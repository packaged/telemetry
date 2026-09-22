package telemetry

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel/trace"
)

// CallContext returns the context for an outbound call made directly on behalf
// of an HTTP request. It carries the request's trace, so the call nests under
// it, but not its cancellation — matching the context.Background() these call
// sites historically used, so a client disconnecting does not abort work that
// other requests may be waiting on.
func CallContext(r *http.Request) context.Context {
	return context.WithoutCancel(RequestContext(r))
}

// Traced grafts a request's span onto ctx, leaving ctx's own cancellation and
// deadline untouched.
//
// Memoised fetches run on a context the cache owns and accept none from the
// caller, so the span has to be attached to that context rather than the
// context replaced. A value later served from the cache produces no span at
// all, so the fetch is attributed to the request that actually paid for it.
func Traced(ctx context.Context, r *http.Request) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	span := trace.SpanFromContext(RequestContext(r))
	if !span.SpanContext().IsValid() {
		return ctx
	}
	return trace.ContextWithSpan(ctx, span)
}

// RequestContext is r's context, or a background context when there is no
// request — instances built outside one, such as CLI commands and tests.
func RequestContext(r *http.Request) context.Context {
	if r == nil {
		return context.Background()
	}
	if ctx := r.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
