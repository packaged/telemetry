package telemetry

import (
	"context"
	"log/slog"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

// Logger returns an slog.Logger that writes to both the local handler and the
// collector, stamping each record with the trace it happened in.
//
// When Init installed nothing, the returned logger is local only.
func Logger(local slog.Handler) *slog.Logger {
	if local == nil {
		local = slog.Default().Handler()
	}
	if loggerProvider == nil {
		return slog.New(local)
	}
	return slog.New(&otelHandler{
		local:  local,
		logger: loggerProvider.Logger(serviceName),
	})
}

// SetDefaultLogger installs Logger as slog's default.
func SetDefaultLogger(local slog.Handler) { slog.SetDefault(Logger(local)) }

// otelHandler fans a record out to the local handler and to OTLP.
type otelHandler struct {
	local  slog.Handler
	logger otellog.Logger
	attrs  []slog.Attr
	group  string
}

func (h *otelHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.local.Enabled(ctx, l)
}

func (h *otelHandler) Handle(ctx context.Context, r slog.Record) error {
	var rec otellog.Record
	rec.SetTimestamp(r.Time)
	rec.SetSeverity(severity(r.Level))
	rec.SetSeverityText(r.Level.String())
	rec.SetBody(attribute.StringValue(r.Message))

	var attrs []attribute.KeyValue
	for _, a := range h.attrs {
		attrs = appendAttr(attrs, h.group, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs = appendAttr(attrs, h.group, a)
		return true
	})
	rec.AddAttributes(attrs...)

	// Emit carries the span from ctx, which is what links this line to its
	// trace in the console.
	h.logger.Emit(ctx, rec)

	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.local.Handle(ctx, r)
}

func (h *otelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &otelHandler{
		local:  h.local.WithAttrs(attrs),
		logger: h.logger,
		attrs:  append(slices.Clip(h.attrs), attrs...),
		group:  h.group,
	}
}

func (h *otelHandler) WithGroup(name string) slog.Handler {
	group := name
	if h.group != "" {
		group = h.group + "." + name
	}
	return &otelHandler{
		local:  h.local.WithGroup(name),
		logger: h.logger,
		attrs:  h.attrs,
		group:  group,
	}
}

// appendAttr flattens a slog attribute onto attrs. Groups become dotted keys,
// since the attribute package has no map value.
func appendAttr(attrs []attribute.KeyValue, group string, a slog.Attr) []attribute.KeyValue {
	key := a.Key
	if group != "" {
		key = group + "." + key
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, sub := range a.Value.Group() {
			attrs = appendAttr(attrs, key, sub)
		}
		return attrs
	}
	return append(attrs, attribute.KeyValue{Key: attribute.Key(key), Value: value(a.Value)})
}

func value(v slog.Value) attribute.Value {
	switch v.Kind() {
	case slog.KindBool:
		return attribute.BoolValue(v.Bool())
	case slog.KindInt64:
		return attribute.Int64Value(v.Int64())
	case slog.KindUint64:
		return attribute.Int64Value(int64(v.Uint64()))
	case slog.KindFloat64:
		return attribute.Float64Value(v.Float64())
	case slog.KindDuration:
		return attribute.Int64Value(int64(v.Duration()))
	default:
		return attribute.StringValue(v.String())
	}
}

// severity maps slog levels onto the OTel severity scale.
func severity(l slog.Level) otellog.Severity {
	switch {
	case l < slog.LevelInfo:
		return otellog.SeverityDebug
	case l < slog.LevelWarn:
		return otellog.SeverityInfo
	case l < slog.LevelError:
		return otellog.SeverityWarn
	default:
		return otellog.SeverityError
	}
}
