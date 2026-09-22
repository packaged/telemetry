package telemetry

import (
	"context"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ServerOptions returns the gRPC server options that turn each inbound call
// into a span, continuing the caller's trace from request metadata.
//
// This is the join for a caller wrapped with DialOptions: its client span and
// this server span land in one trace.
func ServerOptions() []grpc.ServerOption {
	if !Enabled() {
		return nil
	}
	return []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	}
}

// DialOptions returns the dial options that trace outbound gRPC calls and carry
// traceparent to the callee.
//
// A call made outside a trace produces no span: the sampler drops parentless
// client spans rather than starting a trace per call.
func DialOptions() []grpc.DialOption {
	if !Enabled() {
		return nil
	}
	return []grpc.DialOption{
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	}
}

// correlationCarrier is satisfied by any generated message with a
// correlation_id field, which is how a correlation id reaches most services.
type correlationCarrier interface {
	GetCorrelationId() string
}

// CorrelationUnaryInterceptor tags each call with its correlation id, taken
// from the request message's correlation_id field, or failing that from the
// first of metadataKeys present.
//
// The message is tried first because it is the authoritative copy: metadata is
// only set on the hops that bother to forward it.
func CorrelationUnaryInterceptor(metadataKeys ...string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(TagCorrelationID(ctx, correlationID(ctx, req, metadataKeys)), req)
	}
}

func correlationID(ctx context.Context, req any, metadataKeys []string) string {
	if carrier, ok := req.(correlationCarrier); ok {
		if id := carrier.GetCorrelationId(); id != "" {
			return id
		}
	}
	if len(metadataKeys) == 0 {
		return ""
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, key := range metadataKeys {
		for _, val := range md.Get(key) {
			if val != "" {
				return val
			}
		}
	}
	return ""
}
