package alerts

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/pooler"
)

// A pooler counts as down only after failing for the grace period.
func TestPoolerGrace(t *testing.T) {
	ghost, err := pooler.NewAdmin("ghost", "127.0.0.1:1", "pgdock", "x", "disable")
	if err != nil {
		t.Fatal(err)
	}
	s := New(nil, nil, Config{Poolers: []*pooler.Admin{ghost}, PoolerGrace: 200 * time.Millisecond}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	if c := s.poolerConditions(ctx); len(c) != 0 {
		t.Fatalf("down on the first failure: %+v", c)
	}
	time.Sleep(250 * time.Millisecond)
	if c := s.poolerConditions(ctx); len(c) != 1 || c[0].kind != KindPoolerDown || c[0].targetID != "ghost" {
		t.Fatalf("after the grace period: %+v", c)
	}
}
