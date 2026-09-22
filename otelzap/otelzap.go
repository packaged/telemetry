// Package otelzap ships zap log entries to OTLP alongside their existing
// destination, so a log line can be read next to the trace it belongs to.
//
// zap hands Write no context, and the loggers these services use
// (logger.I().Info(msg, fields...)) take none either, so the trace a line
// belongs to cannot be recovered at write time. It has to arrive as a field:
// log TraceIDField, and the entry is attached to that trace.
package otelzap

import (
	"context"
	"fmt"
	"sync"

	"github.com/packaged/telemetry"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Fields naming the trace an entry belongs to. A line logged without them is
// still exported, just unattached to any trace.
const (
	TraceIDField = "trace-id"
	SpanIDField  = "span-id"
)

// WrapCore returns a zap option that tees entries to OTLP as well as to the
// logger's existing core. It is inert when telemetry is not configured, so it
// is safe to apply unconditionally.
//
//	zapper.WithOptions(otelzap.WrapCore("checkout"))
func WrapCore(name string) zap.Option {
	return zap.WrapCore(func(inner zapcore.Core) zapcore.Core {
		if !telemetry.Enabled() {
			return inner
		}
		return zapcore.NewTee(inner, NewCore(name, inner))
	})
}

// NewCore returns a core that exports entries to OTLP. It logs at whatever
// level enabler reports, so it stays in step with the core it accompanies.
func NewCore(name string, enabler zapcore.LevelEnabler) zapcore.Core {
	return &otelCore{LevelEnabler: enabler, resolve: &resolver{name: name}}
}

// resolver defers looking up the logger until the first entry is written.
// A logger built now would be the no-op one whenever logging is configured
// before telemetry, which is the usual order.
type resolver struct {
	name string
	once sync.Once
	log  otellog.Logger
}

func (r *resolver) logger() otellog.Logger {
	r.once.Do(func() { r.log = global.GetLoggerProvider().Logger(r.name) })
	return r.log
}

type otelCore struct {
	zapcore.LevelEnabler
	resolve *resolver
	fields  []zapcore.Field
}

func (c *otelCore) With(fields []zapcore.Field) zapcore.Core {
	return &otelCore{
		LevelEnabler: c.LevelEnabler,
		resolve:      c.resolve,
		fields:       append(append(make([]zapcore.Field, 0, len(c.fields)+len(fields)), c.fields...), fields...),
	}
}

func (c *otelCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *otelCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	var rec otellog.Record
	rec.SetTimestamp(ent.Time)
	rec.SetSeverity(severity(ent.Level))
	rec.SetSeverityText(ent.Level.String())
	rec.SetBody(attribute.StringValue(ent.Message))

	all := fields
	if len(c.fields) > 0 {
		all = append(append(make([]zapcore.Field, 0, len(c.fields)+len(fields)), c.fields...), fields...)
	}

	enc := zapcore.NewMapObjectEncoder()
	for _, f := range all {
		f.AddTo(enc)
	}
	if ent.LoggerName != "" {
		enc.Fields["logger"] = ent.LoggerName
	}
	if ent.Caller.Defined {
		enc.Fields["caller"] = ent.Caller.TrimmedPath()
	}
	if ent.Stack != "" {
		enc.Fields["stacktrace"] = ent.Stack
	}

	var attrs []attribute.KeyValue
	for k, v := range enc.Fields {
		attrs = appendAttr(attrs, k, v)
	}
	rec.AddAttributes(attrs...)

	c.resolve.logger().Emit(traceContext(enc.Fields), rec)
	return nil
}

func (c *otelCore) Sync() error { return nil }

// traceContext rebuilds the span context an entry names, so the exported
// record joins that trace rather than floating loose.
func traceContext(fields map[string]any) context.Context {
	raw, _ := fields[TraceIDField].(string)
	traceID, err := trace.TraceIDFromHex(raw)
	if err != nil {
		return context.Background()
	}

	cfg := trace.SpanContextConfig{TraceID: traceID, TraceFlags: trace.FlagsSampled}
	if raw, ok := fields[SpanIDField].(string); ok {
		if spanID, err := trace.SpanIDFromHex(raw); err == nil {
			cfg.SpanID = spanID
		}
	}
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(cfg))
}

// appendAttr flattens a zap field onto attrs. Nested objects become dotted
// keys, since the attribute package has no map value.
func appendAttr(attrs []attribute.KeyValue, key string, v any) []attribute.KeyValue {
	switch val := v.(type) {
	case nil:
		return attrs
	case string:
		return append(attrs, attribute.String(key, val))
	case bool:
		return append(attrs, attribute.Bool(key, val))
	case int:
		return append(attrs, attribute.Int(key, val))
	case int8:
		return append(attrs, attribute.Int64(key, int64(val)))
	case int16:
		return append(attrs, attribute.Int64(key, int64(val)))
	case int32:
		return append(attrs, attribute.Int64(key, int64(val)))
	case int64:
		return append(attrs, attribute.Int64(key, val))
	case uint:
		return append(attrs, attribute.Int64(key, int64(val)))
	case uint8:
		return append(attrs, attribute.Int64(key, int64(val)))
	case uint16:
		return append(attrs, attribute.Int64(key, int64(val)))
	case uint32:
		return append(attrs, attribute.Int64(key, int64(val)))
	case uint64:
		return append(attrs, attribute.Int64(key, int64(val)))
	case float32:
		return append(attrs, attribute.Float64(key, float64(val)))
	case float64:
		return append(attrs, attribute.Float64(key, val))
	case map[string]any:
		for k, sub := range val {
			attrs = appendAttr(attrs, key+"."+k, sub)
		}
		return attrs
	case error:
		return append(attrs, attribute.String(key, val.Error()))
	case zapcore.ObjectMarshaler:
		enc := zapcore.NewMapObjectEncoder()
		if err := val.MarshalLogObject(enc); err != nil {
			return append(attrs, attribute.String(key, err.Error()))
		}
		for k, sub := range enc.Fields {
			attrs = appendAttr(attrs, key+"."+k, sub)
		}
		return attrs
	default:
		return append(attrs, attribute.String(key, fmt.Sprintf("%v", val)))
	}
}

func severity(l zapcore.Level) otellog.Severity {
	switch {
	case l <= zapcore.DebugLevel:
		return otellog.SeverityDebug
	case l == zapcore.InfoLevel:
		return otellog.SeverityInfo
	case l == zapcore.WarnLevel:
		return otellog.SeverityWarn
	case l == zapcore.ErrorLevel:
		return otellog.SeverityError
	default:
		return otellog.SeverityFatal
	}
}
