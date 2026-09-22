package telemetry

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestShouldTraceSkipsNoise(t *testing.T) {
	skipped := []string{
		"/assets/r/a1b2c3d4/index.min.js",
		"/assets/p/e5f6a7b8/images/banner.png",
		"/images/logo.svg",
		"/favicon.ico",
		"/_ah/health",
		"/health",
		"/.well-known/security.txt",
		"/some/path/style.css",
		"/fonts/body.woff2",
	}
	for _, p := range skipped {
		if shouldTrace(httptest.NewRequest("GET", p, nil)) {
			t.Errorf("%s was traced; asset and probe traffic drowns real requests", p)
		}
	}
}

func TestShouldTraceKeepsRealTraffic(t *testing.T) {
	traced := []string{
		"/",
		"/checkout",
		"/api/orders",
		"/embed/widget",
		"/about",
		"/api/sessions",
	}
	for _, p := range traced {
		if !shouldTrace(httptest.NewRequest("GET", p, nil)) {
			t.Errorf("%s was skipped, but it is exactly what someone debugs", p)
		}
	}
}

func TestShouldTraceHonoursExtraPrefixes(t *testing.T) {
	t.Setenv(extraUntracedEnv, "/internal/, /noisy")

	// The package resolves this once at init, so re-resolve for the test.
	saved := extraUntracedPrefixes
	extraUntracedPrefixes = parseUntraced(os.Getenv(extraUntracedEnv))
	t.Cleanup(func() { extraUntracedPrefixes = saved })

	if shouldTrace(httptest.NewRequest("GET", "/internal/metrics", nil)) {
		t.Error("configured prefix was still traced")
	}
	if shouldTrace(httptest.NewRequest("GET", "/noisy/thing", nil)) {
		t.Error("configured prefix was still traced")
	}
	if !shouldTrace(httptest.NewRequest("GET", "/checkout", nil)) {
		t.Error("configured prefixes must not affect other paths")
	}
}

func TestShouldTraceIgnoresQueryString(t *testing.T) {
	if shouldTrace(httptest.NewRequest("GET", "/assets/app.js?v=2", nil)) {
		t.Error("a cache-busting query string must not make an asset traced")
	}
}

func TestHandlerIsPassThroughWhenDisabled(t *testing.T) {
	t.Setenv(endpointEnv, "")

	orig := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	got, ok := Handler(orig).(http.HandlerFunc)
	if !ok {
		t.Fatal("expected the original handler back when telemetry is off")
	}
	// Comparing funcs directly is not allowed, so compare behaviour instead.
	rec := httptest.NewRecorder()
	got.ServeHTTP(rec, httptest.NewRequest("GET", "/checkout", nil))
	if rec.Code != 200 {
		t.Errorf("pass-through handler returned %d", rec.Code)
	}
}
