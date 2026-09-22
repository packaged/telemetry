# packaged/telemetry

OpenTelemetry wiring for Go, speaking OTLP/HTTP to whatever
`OTEL_EXPORTER_OTLP_ENDPOINT` points at. With no endpoint set, `Init` installs
nothing and every call here is a no-op, so the same build runs unchanged with
nothing listening.

```go
shutdown, err := telemetry.Init(ctx, telemetry.WithService("checkout"))
defer shutdown(ctx)

http.ListenAndServe(":8080", telemetry.Handler(mux))   // server spans
grpc.NewServer(telemetry.ServerOptions()...)           // gRPC server spans
grpc.NewClient(addr, telemetry.DialOptions()...)       // traceparent on outbound
resp, _ := telemetry.Client(nil).Do(req)               // traceparent on outbound
```

Traces, metrics and logs all export.

## Logs

`Logger` bridges `log/slog` onto OTLP. For zap — which `packaged/logger` is
built on — the `otelzap` subpackage tees a logger into OTLP alongside its
existing output:

```go
zapper = zapper.WithOptions(otelzap.WrapCore("checkout"))
```

zap hands `Write` no context, and `logger.I().Info(msg, fields...)` takes none
either, so the trace a line belongs to cannot be recovered at write time. It
has to arrive as a field — log `otelzap.TraceIDField` and the entry is attached
to that trace. Lines logged without it still export, just unattached.

## Correlation IDs

A correlation id minted upstream is searchable as the `correlation_id` span
attribute. Tag it once where the request enters a service; every span that
service goes on to produce carries it.

```go
// HTTP: wrap outside Handler, so the id is set before the server span opens.
telemetry.CorrelationHandler(telemetry.Handler(mux), "X-Correlation-ID")

// gRPC: reads the request's correlation_id field, or the named metadata keys.
grpc.NewServer(grpc.ChainUnaryInterceptor(
    telemetry.CorrelationUnaryInterceptor("Correlation-Id"),
))

// Anywhere else: stamps the open span and every span started beneath ctx.
ctx = telemetry.TagCorrelationID(ctx, id)
```

`TagCorrelationID` stamps the span already in flight as well as seeding the
context. Server spans are opened by the instrumentation before any application
code runs, so the span representing the inbound request exists before its
correlation id has been read — seeding alone would miss it.

## Sampling

A client span with no parent is dropped rather than becoming the root of a
single-span trace: gRPC and HTTP clients are instrumented at the connection, so
background work would otherwise bury real requests. Server spans are never
dropped. Static assets and health probes are not traced; add prefixes with
`OTEL_UNTRACED_PATH_PREFIXES`.

## Tests

```sh
go test ./...
```
