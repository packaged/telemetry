package telemetry

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestCallContextCarriesTheRequestTrace(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(context.Background(), "GET /checkout")
	r := httptest.NewRequest("GET", "/checkout", nil).WithContext(ctx)

	got := trace.SpanContextFromContext(CallContext(r))
	if got.TraceID() != span.SpanContext().TraceID() {
		t.Errorf("call context trace = %s, want the request's %s",
			got.TraceID(), span.SpanContext().TraceID())
	}
	if got.SpanID() != span.SpanContext().SpanID() {
		t.Error("outbound call would not nest under the request span")
	}
}

func TestCallContextIgnoresRequestCancellation(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	ctx, cancel := context.WithCancel(context.Background())
	ctx, _ = tp.Tracer("test").Start(ctx, "GET /checkout")
	r := httptest.NewRequest("GET", "/checkout", nil).WithContext(ctx)

	callCtx := CallContext(r)
	cancel()

	// These call sites used context.Background() before; a client disconnecting
	// must not abort work other requests may be waiting on.
	select {
	case <-callCtx.Done():
		t.Fatal("call context was cancelled with the request")
	case <-time.After(20 * time.Millisecond):
	}
	if _, ok := callCtx.Deadline(); ok {
		t.Error("call context should carry no deadline")
	}
}

func TestTracedKeepsTheCachesCancellation(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	reqCtx, cancelReq := context.WithCancel(context.Background())
	reqCtx, span := tp.Tracer("test").Start(reqCtx, "GET /checkout")
	r := httptest.NewRequest("GET", "/checkout", nil).WithContext(reqCtx)

	// The memoiser owns this context and accepts none from the caller.
	cacheCtx, cancelCache := context.WithCancel(context.Background())
	fetchCtx := Traced(cacheCtx, r)

	if got := trace.SpanContextFromContext(fetchCtx).TraceID(); got != span.SpanContext().TraceID() {
		t.Errorf("memoised fetch trace = %s, want %s", got, span.SpanContext().TraceID())
	}

	cancelReq()
	select {
	case <-fetchCtx.Done():
		t.Fatal("a shared fetch was cancelled by one request going away")
	case <-time.After(20 * time.Millisecond):
	}

	cancelCache()
	select {
	case <-fetchCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("the cache lost control of its own context")
	}
}

func TestRequestHelpersAreSafeWithoutARequest(t *testing.T) {
	if CallContext(nil) == nil {
		t.Error("CallContext(nil) returned a nil context")
	}
	if Traced(nil, nil) == nil {
		t.Error("Traced(nil, nil) returned a nil context")
	}
	if RequestContext(nil) == nil {
		t.Error("RequestContext(nil) returned a nil context")
	}
}

func TestTracedLeavesContextAloneWhenUntraced(t *testing.T) {
	// No telemetry configured: no span on the request, so nothing to graft.
	r := httptest.NewRequest("GET", "/checkout", nil)

	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "kept")

	if got := Traced(ctx, r); got.Value(key{}) != "kept" {
		t.Error("Traced dropped the cache's context values")
	}
}
