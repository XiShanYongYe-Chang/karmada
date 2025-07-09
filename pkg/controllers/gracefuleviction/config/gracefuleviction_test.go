package config

import (
	"github.com/spf13/pflag"
	"testing"
)

func TestEvictionControllerOptions_AddFlags(t *testing.T) {
	config := &GracefulEvictionOptions{}
	fs := pflag.NewFlagSet("test", pflag.ExitOnError)
	config.AddFlags(fs)

	expectedFlags := []string{
		"resource-eviction-rate",
		"secondary-resource-eviction-rate",
		"unhealthy-cluster-threshold",
		"large-cluster-num-threshold",
	}

	for _, flagName := range expectedFlags {
		if fs.Lookup(flagName) == nil {
			t.Errorf("Expected flag %s not found", flagName)
		}
	}
}

func TestEvictionControllerOptions_AddFlags_NilReceiver(t *testing.T) {
	var config *GracefulEvictionOptions
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)

	config.AddFlags(fs)

	if fs.HasFlags() {
		t.Error("Expected no flags to be added when receiver is nil, but flags were added")
	}
}
