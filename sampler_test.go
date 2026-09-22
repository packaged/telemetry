package telemetry

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func sampler() sdktrace.Sampler {
	return dropRootlessClientSpans{inner: sdktrace.AlwaysSample()}
}

// parentCtx returns a context carrying a valid remote parent span.
func parentCtx() context.Context {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01},
		SpanID:     trace.SpanID{0x02},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	return trace.ContextWithSpanContext(context.Background(), sc)
}

func TestRootlessClientSpanIsDropped(t *testing.T) {
	got := sampler().ShouldSample(sdktrace.SamplingParameters{
		ParentContext: context.Background(),
		Kind:          trace.SpanKindClient,
		Name:          "auth.AuthService/Resolve",
	})
	if got.Decision != sdktrace.Drop {
		t.Errorf("decision = %v, want Drop: a parentless client call would start its own trace", got.Decision)
	}
}

func TestClientSpanWithAParentIsKept(t *testing.T) {
	got := sampler().ShouldSample(sdktrace.SamplingParameters{
		ParentContext: parentCtx(),
		Kind:          trace.SpanKindClient,
		Name:          "auth.AuthService/Resolve",
	})
	if got.Decision == sdktrace.Drop {
		t.Error("a client call inside a request must be recorded")
	}
}

func TestRootlessServerSpanIsKept(t *testing.T) {
	// An inbound request legitimately starts a trace.
	got := sampler().ShouldSample(sdktrace.SamplingParameters{
		ParentContext: context.Background(),
		Kind:          trace.SpanKindServer,
		Name:          "GET /checkout",
	})
	if got.Decision == sdktrace.Drop {
		t.Error("an inbound request must start a trace")
	}
}

func TestRootlessInternalSpanIsKept(t *testing.T) {
	// Background jobs that deliberately open a span keep working.
	got := sampler().ShouldSample(sdktrace.SamplingParameters{
		ParentContext: context.Background(),
		Kind:          trace.SpanKindInternal,
		Name:          "nightly-reconcile",
	})
	if got.Decision == sdktrace.Drop {
		t.Error("an explicitly started root span must be recorded")
	}
}

func TestDescriptionNamesTheInnerSampler(t *testing.T) {
	if got := sampler().Description(); got == "" {
		t.Error("sampler description must not be empty")
	}
}
