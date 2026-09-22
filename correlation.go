package telemetry

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// AttrCorrelationID is the span attribute every span in a correlated request
// carries. Backends index and search on this key.
const AttrCorrelationID = "correlation_id"

type correlationKey struct{}

// ContextWithCorrelationID returns a context carrying id, so that every span
// started beneath it is stamped with AttrCorrelationID.
func ContextWithCorrelationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationIDFromContext returns the correlation id carried by ctx, or "".
func CorrelationIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

// TagCorrelationID records id for this request: it stamps the span already in
// flight and returns a context that stamps every span started beneath it.
//
// Both halves are needed. Server spans are opened by the HTTP and gRPC
// instrumentation before any application code runs, so the span that
// represents the inbound request exists before its correlation id has been
// read off the request — the processor alone would never see it.
func TagCorrelationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.SetAttributes(attribute.String(AttrCorrelationID, id))
	}
	return ContextWithCorrelationID(ctx, id)
}

// correlationProcessor copies the correlation id from a span's parent context
// onto the span as it starts, so a single call at the edge of a service tags
// everything that service goes on to do.
type correlationProcessor struct{}

func (correlationProcessor) OnStart(parent context.Context, s sdktrace.ReadWriteSpan) {
	if id := CorrelationIDFromContext(parent); id != "" {
		s.SetAttributes(attribute.String(AttrCorrelationID, id))
	}
}

func (correlationProcessor) OnEnd(sdktrace.ReadOnlySpan)      {}
func (correlationProcessor) Shutdown(context.Context) error   { return nil }
func (correlationProcessor) ForceFlush(context.Context) error { return nil }

// CorrelationHandler reads a correlation id from the first of headers to carry
// one and tags the request with it. Wrap it *outside* Handler so the id is in
// place before the server span is created.
func CorrelationHandler(h http.Handler, headers ...string) http.Handler {
	if len(headers) == 0 {
		headers = []string{"X-Correlation-ID"}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range headers {
			if id := r.Header.Get(name); id != "" {
				r = r.WithContext(TagCorrelationID(r.Context(), id))
				break
			}
		}
		h.ServeHTTP(w, r)
	})
}
