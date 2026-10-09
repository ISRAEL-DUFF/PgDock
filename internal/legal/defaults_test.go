package legal

import (
	"strings"
	"testing"

	"github.com/israel-duff/pgdock/internal/messaging"
)

// TestDPAListsMessageProviders: every message provider PGDock holds its own
// account with is a sub-processor in the default DPA (V4 §2.5).
func TestDPAListsMessageProviders(t *testing.T) {
	for _, p := range messaging.PlatformProviders {
		name, ok := SubProcessors[p.Name()]
		if !ok {
			t.Errorf("platform provider %q has no entry in legal.SubProcessors", p.Name())
			continue
		}
		if !strings.Contains(DefaultDPA, "| "+name+" |") {
			t.Errorf("the default DPA doesn't list %s (%s) as a sub-processor", name, p.Name())
		}
	}
}
