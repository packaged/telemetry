// Package telemetry wires an application's OpenTelemetry providers to an OTLP
// endpoint. It is a thin, opinionated layer over the OTel SDK: nothing here is
// tied to a particular backend, so pointing OTEL_EXPORTER_OTLP_ENDPOINT at a
// different one keeps working.
//
// With no endpoint configured Init installs nothing and every call here is a
// no-op, so a binary behaves identically when nothing is listening.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	endpointEnv = "OTEL_EXPORTER_OTLP_ENDPOINT"
	serviceEnv  = "OTEL_SERVICE_NAME"

	// unknownService is what OTLP recommends when service.name is unset.
	unknownService = "unknown_service"
)

// Shutdown flushes the providers. Init never returns a nil Shutdown, so the
// caller can defer it without a nil check even when initialisation failed.
type Shutdown func(context.Context) error

func noopShutdown(context.Context) error { return nil }

type options struct {
	service    string
	version    string
	endpoint   string
	env        string
	metricTick time.Duration
	attrs      []attribute.KeyValue
}

// Option customises Init. Everything has an environment-variable default so
// deployments need no code changes.
type Option func(*options)

// WithService sets the fallback service name, used when OTEL_SERVICE_NAME is
// not set. Deployments set the environment variable, so this is the local-run
// default rather than an override.
func WithService(name string) Option { return func(o *options) { o.service = name } }

// WithVersion sets service.version.
func WithVersion(v string) Option { return func(o *options) { o.version = v } }

// WithEndpoint sets the OTLP base URL, overriding OTEL_EXPORTER_OTLP_ENDPOINT.
func WithEndpoint(url string) Option { return func(o *options) { o.endpoint = url } }

// WithEnvironment sets deployment.environment.
func WithEnvironment(env string) Option { return func(o *options) { o.env = env } }

// WithMetricInterval sets how often metrics are exported.
func WithMetricInterval(d time.Duration) Option { return func(o *options) { o.metricTick = d } }

// WithAttributes adds resource attributes reported with every signal.
func WithAttributes(kv ...attribute.KeyValue) Option {
	return func(o *options) { o.attrs = append(o.attrs, kv...) }
}

// serviceName is what this process reports as, resolved once by Init.
var serviceName = unknownService

var loggerProvider *sdklog.LoggerProvider

// Enabled reports whether telemetry is configured. It reads the environment,
// which Init keeps in step with WithEndpoint, so it is only meaningful once
// Init has run.
func Enabled() bool { return os.Getenv(endpointEnv) != "" }

// ServiceName is what this process reports as.
func ServiceName() string { return serviceName }

// Init installs the global tracer, meter and logger providers and returns a
// shutdown func that flushes them.
func Init(ctx context.Context, opts ...Option) (Shutdown, error) {
	o := options{
		version:    envOr("SERVICE_VERSION", "dev"),
		env:        envOr("DEPLOYMENT_ENVIRONMENT", "development"),
		endpoint:   os.Getenv(endpointEnv),
		metricTick: 15 * time.Second,
	}
	for _, fn := range opts {
		fn(&o)
	}

	serviceName = envOr(serviceEnv, o.service)
	if serviceName == "" {
		serviceName = unknownService
	}

	if o.endpoint == "" {
		return noopShutdown, nil
	}
	// The exporters read OTEL_EXPORTER_OTLP_ENDPOINT themselves; setting it
	// keeps WithEndpoint and the environment on one code path, and is what
	// makes Enabled truthful after a WithEndpoint-only Init.
	os.Setenv(endpointEnv, o.endpoint)

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		resource.Default().SchemaURL(),
		append([]attribute.KeyValue{
			attribute.String("service.name", serviceName),
			attribute.String("service.version", o.version),
			attribute.String("deployment.environment", o.env),
		}, o.attrs...)...,
	))
	if err != nil {
		return noopShutdown, fmt.Errorf("build resource: %w", err)
	}

	var shutdowns []func(context.Context) error
	abort := func(err error) (Shutdown, error) {
		_ = flush(context.Background(), shutdowns)
		return noopShutdown, err
	}

	traceExp, err := otlptracehttp.New(ctx)
	if err != nil {
		return abort(fmt.Errorf("trace exporter: %w", err))
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(correlationProcessor{}),
		sdktrace.WithSampler(dropRootlessClientSpans{
			inner: sdktrace.ParentBased(sdktrace.AlwaysSample()),
		}),
	)
	otel.SetTracerProvider(tp)
	shutdowns = append(shutdowns, tp.Shutdown)

	metricExp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return abort(fmt.Errorf("metric exporter: %w", err))
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(o.metricTick))),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)
	shutdowns = append(shutdowns, mp.Shutdown)

	logExp, err := otlploghttp.New(ctx)
	if err != nil {
		return abort(fmt.Errorf("log exporter: %w", err))
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	loggerProvider = lp
	global.SetLoggerProvider(lp)
	shutdowns = append(shutdowns, lp.Shutdown)

	// W3C tracecontext both ways, so a span started here continues in whatever
	// service this one calls — including the PHP adapter, which speaks it too.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
		return abort(fmt.Errorf("runtime metrics: %w", err))
	}

	return func(ctx context.Context) error { return flush(ctx, shutdowns) }, nil
}

func flush(ctx context.Context, fns []func(context.Context) error) error {
	var errs []error
	for i := len(fns) - 1; i >= 0; i-- {
		if err := fns[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Tracer returns a tracer for the calling component.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }

// Meter returns a meter for the calling component.
func Meter(name string) metric.Meter { return otel.Meter(name) }

// Start begins a span on the service tracer.
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return otel.Tracer(serviceName).Start(ctx, name, opts...)
}

// TraceIDFromContext returns the current trace id, or "" outside a trace.
// Handlers use it to show users an id they can quote back, and log lines use
// it to join a message to its trace.
func TraceIDFromContext(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
