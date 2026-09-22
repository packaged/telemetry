package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// recordingTracer returns a tracer wired to the correlation processor, and the
// exporter holding whatever it produced.
func recordingTracer(t *testing.T) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(correlationProcessor{}),
		sdktrace.WithSyncer(exp),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp, exp
}

func spanCorrelation(t *testing.T, exp *tracetest.InMemoryExporter) (string, bool) {
	t.Helper()
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	for _, a := range spans[0].Attributes {
		if string(a.Key) == AttrCorrelationID {
			return a.Value.AsString(), true
		}
	}
	return "", false
}

func TestProcessorStampsSpansBeneathTaggedContext(t *testing.T) {
	tp, exp := recordingTracer(t)

	ctx := ContextWithCorrelationID(context.Background(), "01JABC")
	_, span := tp.Tracer("test").Start(ctx, "child")
	span.End()

	got, ok := spanCorrelation(t, exp)
	if !ok {
		t.Fatal("span carries no correlation id")
	}
	if got != "01JABC" {
		t.Errorf("correlation id = %q, want 01JABC", got)
	}
}

func TestProcessorLeavesUntaggedSpansAlone(t *testing.T) {
	tp, exp := recordingTracer(t)

	_, span := tp.Tracer("test").Start(context.Background(), "child")
	span.End()

	if got, ok := spanCorrelation(t, exp); ok {
		t.Errorf("untagged span carries correlation id %q", got)
	}
}

// TagCorrelationID has to reach the span that is already open, because server
// spans are created before any code can read the request.
func TestTagCorrelationIDStampsSpanInFlight(t *testing.T) {
	tp, exp := recordingTracer(t)

	ctx, span := tp.Tracer("test").Start(context.Background(), "server")
	TagCorrelationID(ctx, "01JDEF")
	span.End()

	got, ok := spanCorrelation(t, exp)
	if !ok {
		t.Fatal("in-flight span carries no correlation id")
	}
	if got != "01JDEF" {
		t.Errorf("correlation id = %q, want 01JDEF", got)
	}
}

func TestContextRoundTrip(t *testing.T) {
	ctx := ContextWithCorrelationID(context.Background(), "01JGHI")
	if got := CorrelationIDFromContext(ctx); got != "01JGHI" {
		t.Errorf("CorrelationIDFromContext = %q, want 01JGHI", got)
	}
	if got := CorrelationIDFromContext(context.Background()); got != "" {
		t.Errorf("bare context yielded %q, want empty", got)
	}
	if CorrelationIDFromContext(ContextWithCorrelationID(context.Background(), "")) != "" {
		t.Error("empty id should not be stored")
	}
}

type messageWithCorrelation struct{ id string }

func (m messageWithCorrelation) GetCorrelationId() string { return m.id }

func TestInterceptorReadsCorrelationFromMessage(t *testing.T) {
	var seen string
	interceptor := CorrelationUnaryInterceptor("Correlation-Id")
	handler := func(ctx context.Context, _ any) (any, error) {
		seen = CorrelationIDFromContext(ctx)
		return nil, nil
	}

	_, err := interceptor(context.Background(), messageWithCorrelation{id: "01JMSG"},
		&grpc.UnaryServerInfo{}, handler)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if seen != "01JMSG" {
		t.Errorf("correlation id = %q, want 01JMSG", seen)
	}
}

func TestInterceptorFallsBackToMetadata(t *testing.T) {
	var seen string
	interceptor := CorrelationUnaryInterceptor("Correlation-Id")
	handler := func(ctx context.Context, _ any) (any, error) {
		seen = CorrelationIDFromContext(ctx)
		return nil, nil
	}

	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("Correlation-Id", "01JMETA"))

	// A message without the field, so only metadata can answer.
	_, err := interceptor(ctx, struct{}{}, &grpc.UnaryServerInfo{}, handler)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if seen != "01JMETA" {
		t.Errorf("correlation id = %q, want 01JMETA", seen)
	}
}

func TestInterceptorPrefersMessageOverMetadata(t *testing.T) {
	var seen string
	interceptor := CorrelationUnaryInterceptor("Correlation-Id")
	handler := func(ctx context.Context, _ any) (any, error) {
		seen = CorrelationIDFromContext(ctx)
		return nil, nil
	}

	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("Correlation-Id", "01JMETA"))

	_, err := interceptor(ctx, messageWithCorrelation{id: "01JMSG"}, &grpc.UnaryServerInfo{}, handler)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if seen != "01JMSG" {
		t.Errorf("correlation id = %q, want 01JMSG", seen)
	}
}

func TestInterceptorWithoutCorrelationIsInert(t *testing.T) {
	called := false
	interceptor := CorrelationUnaryInterceptor()
	handler := func(ctx context.Context, _ any) (any, error) {
		called = true
		if got := CorrelationIDFromContext(ctx); got != "" {
			t.Errorf("correlation id = %q, want empty", got)
		}
		return nil, nil
	}

	if _, err := interceptor(context.Background(), struct{}{}, &grpc.UnaryServerInfo{}, handler); err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if !called {
		t.Error("handler was not called")
	}
}

func TestCorrelationHandlerReadsFirstPresentHeader(t *testing.T) {
	var seen string
	h := CorrelationHandler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = CorrelationIDFromContext(r.Context())
	}), "X-Correlation-ID", "X-Request-Correlation-ID")

	req := httptest.NewRequest(http.MethodGet, "/auth", nil)
	req.Header.Set("X-Request-Correlation-ID", "01JHDR")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "01JHDR" {
		t.Errorf("correlation id = %q, want 01JHDR", seen)
	}
}

func TestCorrelationHandlerWithoutHeaderIsInert(t *testing.T) {
	served := false
	h := CorrelationHandler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		served = true
		if got := CorrelationIDFromContext(r.Context()); got != "" {
			t.Errorf("correlation id = %q, want empty", got)
		}
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/auth", nil))
	if !served {
		t.Error("handler was not called")
	}
}
