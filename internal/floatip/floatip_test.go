package floatip

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestHetznerAgainstFake(t *testing.T) {
	fake := &Fake{Token: "tok", IPID: "42"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	h := &Hetzner{API: srv.URL + "/v1", Token: "tok", IPID: "42"}

	if got, err := h.Holder(ctx); err != nil || got != "" {
		t.Fatalf("unassigned holder: %q, %v", got, err)
	}
	if err := h.Assign(ctx, "1001"); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.Holder(ctx); got != "1001" {
		t.Fatalf("holder after assign: %q", got)
	}
	// Assigning to the holder again does not call the API.
	if err := h.Assign(ctx, "1001"); err != nil {
		t.Fatal(err)
	}
	if err := h.Assign(ctx, "1002"); err != nil {
		t.Fatal(err)
	}
	if got := fake.Assignments(); len(got) != 2 || got[0] != 1001 || got[1] != 1002 {
		t.Fatalf("assignments: %v", got)
	}

	if err := (&Hetzner{API: srv.URL + "/v1", Token: "wrong", IPID: "42"}).Assign(ctx, "1"); err == nil {
		t.Fatal("a wrong token was accepted")
	}
	if _, err := (&Hetzner{API: srv.URL + "/v1", Token: "tok", IPID: "7"}).Holder(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown IP: %v", err)
	}
	if err := h.Assign(ctx, "not-a-number"); err == nil {
		t.Fatal("a non-numeric server ID was accepted")
	}
}
