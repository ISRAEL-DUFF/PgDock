package sandbox

import (
	"net/http/httptest"
	"testing"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/billing/flutterwave"
	"github.com/israel-duff/pgdock/internal/billing/ispend"
)

// TestChecksAgainstFakes runs the sandbox checks against PGDock's fakes of
// both providers, so the checks themselves are exercised in CI.
func TestChecksAgainstFakes(t *testing.T) {
	ffake := flutterwave.NewFake("FLWSECK_TEST-x", "hash")
	fw := httptest.NewServer(ffake)
	defer fw.Close()
	checkProvider(t, flutterwave.New(flutterwave.Config{BaseURL: fw.URL, SecretKey: "FLWSECK_TEST-x", WebhookHash: "hash", BVN: "22222222222"}),
		billing.ChannelCard, "", func(ref, _ string) error { _, err := ffake.CompleteCheckout(ref, true); return err })
	ifake := ispend.NewFake("key", "secret")
	is := httptest.NewServer(ifake)
	defer is.Close()
	checkProvider(t, ispend.New(ispend.Config{BaseURL: is.URL, APIKey: "key", WebhookSecret: "secret"}), billing.ChannelWallet, "",
		func(_, url string) error { _, err := ifake.Approve(url, 0); return err })
}
