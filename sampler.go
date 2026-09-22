package telemetry

import (
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// dropRootlessClientSpans discards a client span that has no parent.
//
// gRPC and HTTP clients are instrumented at the connection, so a call made with
// a context that never carried the request's span — one that slipped past the
// request context, or genuine background work — would otherwise become the root
// of its own single-span trace. Dozens of those bury the real requests.
//
// Server spans are never dropped: an inbound request legitimately starts a
// trace.
type dropRootlessClientSpans struct {
	inner sdktrace.Sampler
}

func (s dropRootlessClientSpans) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	parent := trace.SpanContextFromContext(p.ParentContext)

	if !parent.IsValid() && p.Kind == trace.SpanKindClient {
		return sdktrace.SamplingResult{
			Decision:   sdktrace.Drop,
			Tracestate: parent.TraceState(),
		}
	}
	return s.inner.ShouldSample(p)
}

func (s dropRootlessClientSpans) Description() string {
	return "DropRootlessClientSpans{" + s.inner.Description() + "}"
}
