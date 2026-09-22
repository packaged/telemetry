package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These measure the cost paid by a service with no telemetry configured.

func BenchmarkCallContextDisabled(b *testing.B) {
	b.Setenv(endpointEnv, "")
	r := httptest.NewRequest("GET", "/checkout", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = CallContext(r)
	}
}

func BenchmarkContextBackgroundBaseline(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = context.Background()
	}
}

func BenchmarkTracedDisabled(b *testing.B) {
	b.Setenv(endpointEnv, "")
	r := httptest.NewRequest("GET", "/checkout", nil)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = Traced(ctx, r)
	}
}

func BenchmarkDialOptionsDisabled(b *testing.B) {
	b.Setenv(endpointEnv, "")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = DialOptions()
	}
}

func BenchmarkHandlerServeDisabled(b *testing.B) {
	b.Setenv(endpointEnv, "")
	h := Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	r := httptest.NewRequest("GET", "/checkout", nil)
	// Reuse the recorder: allocating one per iteration would measure the
	// recorder rather than the handler.
	w := httptest.NewRecorder()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		h.ServeHTTP(w, r)
	}
}
