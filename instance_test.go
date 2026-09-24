package telemetry

import (
	"os"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

func TestServiceInstanceIDPrefersTheConfiguredID(t *testing.T) {
	detected := resource.NewSchemaless(attribute.String("service.instance.id", "configured"))
	if got := serviceInstanceID(detected); got != "configured" {
		t.Fatalf("got %q, want configured", got)
	}
}

func TestServiceInstanceIDFallsBackToTheHostName(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Skip("no host name on this machine")
	}
	if got := serviceInstanceID(resource.Empty()); got != host {
		t.Fatalf("got %q, want host name %q", got, host)
	}
}
