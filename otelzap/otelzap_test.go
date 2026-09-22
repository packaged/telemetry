package otelzap

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type recordingExporter struct{ records []sdklog.Record }

func (e *recordingExporter) Export(_ context.Context, records []sdklog.Record) error {
	for _, r := range records {
		e.records = append(e.records, r.Clone())
	}
	return nil
}
func (e *recordingExporter) Shutdown(context.Context) error   { return nil }
func (e *recordingExporter) ForceFlush(context.Context) error { return nil }

// recordingLogger installs a global logger provider and returns a zap logger
// teed into it.
func recordingLogger(t *testing.T) (*zap.Logger, *recordingExporter) {
	t.Helper()
	exp := &recordingExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	global.SetLoggerProvider(lp)

	core := NewCore("test", zapcore.DebugLevel)
	return zap.New(core), exp
}

func attrs(t *testing.T, r sdklog.Record) map[string]string {
	t.Helper()
	out := map[string]string{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		out[string(kv.Key)] = kv.Value.Emit()
		return true
	})
	return out
}

func TestEntryIsExportedWithFields(t *testing.T) {
	log, exp := recordingLogger(t)

	log.Info("charge authorised", zap.String("correlation-id", "01JABC"), zap.Int("attempt", 2))

	if len(exp.records) != 1 {
		t.Fatalf("got %d records, want 1", len(exp.records))
	}
	rec := exp.records[0]
	if got := rec.Body().AsString(); got != "charge authorised" {
		t.Errorf("body = %q, want %q", got, "charge authorised")
	}

	got := attrs(t, rec)
	if got["correlation-id"] != "01JABC" {
		t.Errorf("correlation-id = %q, want 01JABC", got["correlation-id"])
	}
	if got["attempt"] != "2" {
		t.Errorf("attempt = %q, want 2", got["attempt"])
	}
}

// The trace a line belongs to can only arrive as a field, since zap hands
// Write no context.
func TestTraceIDFieldAttachesTheRecordToItsTrace(t *testing.T) {
	log, exp := recordingLogger(t)

	log.Info("charged",
		zap.String(TraceIDField, "4bf92f3577b34da6a3ce929d0e0e4736"),
		zap.String(SpanIDField, "00f067aa0ba902b7"),
	)

	rec := exp.records[0]
	if got := rec.TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id = %q, want 4bf92f3577b34da6a3ce929d0e0e4736", got)
	}
	if got := rec.SpanID().String(); got != "00f067aa0ba902b7" {
		t.Errorf("span id = %q, want 00f067aa0ba902b7", got)
	}
}

func TestEntryWithoutTraceIDIsStillExported(t *testing.T) {
	log, exp := recordingLogger(t)

	log.Warn("no trace here")

	if len(exp.records) != 1 {
		t.Fatalf("got %d records, want 1", len(exp.records))
	}
	if exp.records[0].TraceID().IsValid() {
		t.Error("record claims a trace it was never given")
	}
}

func TestMalformedTraceIDIsIgnored(t *testing.T) {
	log, exp := recordingLogger(t)

	log.Info("bad id", zap.String(TraceIDField, "not-a-trace-id"))

	if exp.records[0].TraceID().IsValid() {
		t.Error("malformed trace id was accepted")
	}
}

func TestWithFieldsAreCarried(t *testing.T) {
	log, exp := recordingLogger(t)

	log.With(zap.String("service", "checkout")).Info("started")

	if got := attrs(t, exp.records[0])["service"]; got != "checkout" {
		t.Errorf("service = %q, want checkout", got)
	}
}

func TestSeverityMapsFromLevel(t *testing.T) {
	log, exp := recordingLogger(t)

	log.Error("failed")

	if got := exp.records[0].SeverityText(); got != "error" {
		t.Errorf("severity text = %q, want error", got)
	}
}

func TestWrapCoreIsInertWithoutTelemetry(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	exp := &recordingExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	global.SetLoggerProvider(lp)

	zap.NewNop().WithOptions(WrapCore("test")).Info("dropped")

	if len(exp.records) != 0 {
		t.Errorf("exported %d records with telemetry disabled, want 0", len(exp.records))
	}
}

func TestWrapCoreTeesWhenEnabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:8080")
	exp := &recordingExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	global.SetLoggerProvider(lp)

	core, observed := observerCore()
	zap.New(core).WithOptions(WrapCore("test")).Info("kept")

	if len(exp.records) != 1 {
		t.Errorf("exported %d records, want 1", len(exp.records))
	}
	if observed() != 1 {
		t.Error("the original core stopped receiving entries")
	}
}

// observerCore returns a core that counts what it is asked to write.
func observerCore() (zapcore.Core, func() int) {
	count := 0
	return zapcore.NewTee(&countingCore{LevelEnabler: zapcore.DebugLevel, count: &count}), func() int { return count }
}

type countingCore struct {
	zapcore.LevelEnabler
	count *int
}

func (c *countingCore) With([]zapcore.Field) zapcore.Core { return c }
func (c *countingCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	return ce.AddCore(ent, c)
}
func (c *countingCore) Write(zapcore.Entry, []zapcore.Field) error { *c.count++; return nil }
func (c *countingCore) Sync() error                                { return nil }
