package runner

import (
	"errors"
	"testing"
)

func TestHarnessCapabilityRequiresJSON(t *testing.T) {
	capability := HarnessCapability{SupportsFilter: true}
	if err := capability.CanIsolate("Sixel", "flat"); !errors.Is(err, ErrUnsupportedHarness) {
		t.Fatalf("error = %v", err)
	}
	capability.SupportsJSON = true
	capability.SupportsBench = true
	if err := capability.CanIsolate("Sixel", "flat"); err != nil {
		t.Fatal(err)
	}
}
