package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestRecorderRegisters(t *testing.T) {
	registry := prometheus.NewRegistry()
	recorder, err := New(registry)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if recorder == nil {
		t.Fatal("expected recorder")
	}
	if _, err := registry.Gather(); err != nil {
		t.Fatalf("Gather returned error: %v", err)
	}
}
