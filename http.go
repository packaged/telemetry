package telemetry

import (
	"net/http"
	"os"
	"path"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Static assets and health probes are requests nobody debugs, and there are far
// more of them than real traffic — tracing them buries the requests that matter.
var untracedPrefixes = []string{
	"/assets/",
	"/images/",
	"/static/",
	"/_ah/",
	"/.well-known/",
}

var untracedExact = []string{
	"/favicon.ico",
	"/robots.txt",
	"/health",
	"/healthz",
	"/readyz",
	"/_ah/health",
}

var untracedExtensions = []string{
	".css", ".js", ".map", ".png", ".jpg", ".jpeg", ".gif", ".svg",
	".webp", ".ico", ".woff", ".woff2", ".ttf", ".eot",
}

// extraUntracedEnv adds comma-separated path prefixes to the lists above,
// which is where deployment-specific paths belong.
const extraUntracedEnv = "OTEL_UNTRACED_PATH_PREFIXES"

// extraUntracedPrefixes is resolved once; shouldTrace runs on every request.
var extraUntracedPrefixes = parseUntraced(os.Getenv(extraUntracedEnv))

// shouldTrace reports whether a request is worth a span.
func shouldTrace(r *http.Request) bool {
	p := r.URL.Path

	for _, exact := range untracedExact {
		if p == exact {
			return false
		}
	}
	for _, prefix := range untracedPrefixes {
		if strings.HasPrefix(p, prefix) {
			return false
		}
	}
	for _, prefix := range extraUntracedPrefixes {
		if strings.HasPrefix(p, prefix) {
			return false
		}
	}

	ext := strings.ToLower(path.Ext(p))
	for _, untraced := range untracedExtensions {
		if ext == untraced {
			return false
		}
	}
	return true
}

func parseUntraced(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Handler wraps the web handler so each request becomes a server span, and an
// incoming traceparent continues the caller's trace. Static assets and health
// probes are skipped.
func Handler(h http.Handler, opts ...otelhttp.Option) http.Handler {
	if !Enabled() {
		return h
	}
	base := []otelhttp.Option{
		otelhttp.WithFilter(shouldTrace),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	}
	return otelhttp.NewHandler(h, "http.server", append(base, opts...)...)
}

// Transport wraps an HTTP transport so outbound requests carry traceparent and
// produce client spans. Pass nil for http.DefaultTransport.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if !Enabled() {
		return base
	}
	return otelhttp.NewTransport(base)
}

// Client returns an instrumented copy of c, or an instrumented default client
// when c is nil. Use it for calls to other services so the trace follows.
func Client(c *http.Client) *http.Client {
	if !Enabled() {
		if c == nil {
			return http.DefaultClient
		}
		return c
	}
	if c == nil {
		return &http.Client{Transport: Transport(nil)}
	}
	clone := *c
	clone.Transport = Transport(c.Transport)
	return &clone
}
