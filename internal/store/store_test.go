package store_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/store/storetest"
)

func TestMigrateIsIdempotent(t *testing.T) {
	pool := storetest.New(t)
	if err := store.Migrate(context.Background(), pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
}

func TestClaimSkipsLockedAndFiltersKinds(t *testing.T) {
	ctx := context.Background()
	q := store.New(storetest.New(t))

	a, err := q.EnqueueOperation(ctx, store.EnqueueOperationParams{Kind: "noop", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.EnqueueOperation(ctx, store.EnqueueOperationParams{Kind: "other", Params: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}

	got, err := q.ClaimOperation(ctx, store.ClaimOperationParams{Worker: "w1", Kinds: []string{"noop"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID || got.Status != "running" || got.Attempts != 1 || got.LockedBy == nil || *got.LockedBy != "w1" {
		t.Fatalf("unexpected claim: %+v", got)
	}
	if _, err := q.ClaimOperation(ctx, store.ClaimOperationParams{Worker: "w2", Kinds: []string{"noop"}}); err == nil {
		t.Fatal("second claim should find nothing")
	}

	// Only the lock holder may write.
	if n, _ := q.SucceedOperation(ctx, store.SucceedOperationParams{ID: a.ID, Worker: "w2"}); n != 0 {
		t.Fatal("non-owner completed the operation")
	}
	if n, _ := q.AppendOperationLog(ctx, store.AppendOperationLogParams{ID: a.ID, Worker: "w1", Entry: json.RawMessage(`{"msg":"hi"}`)}); n != 1 {
		t.Fatal("owner could not append log")
	}
	if n, _ := q.SucceedOperation(ctx, store.SucceedOperationParams{ID: a.ID, Worker: "w1"}); n != 1 {
		t.Fatal("owner could not complete")
	}
	done, err := q.GetOperation(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "succeeded" || done.FinishedAt == nil || done.LockedBy != nil {
		t.Fatalf("unexpected final state: %+v", done)
	}
}

func TestReclaimStale(t *testing.T) {
	ctx := context.Background()
	q := store.New(storetest.New(t))

	op, _ := q.EnqueueOperation(ctx, store.EnqueueOperationParams{Kind: "noop", Params: json.RawMessage(`{}`)})
	if _, err := q.ClaimOperation(ctx, store.ClaimOperationParams{Worker: "dead", Kinds: []string{"noop"}}); err != nil {
		t.Fatal(err)
	}

	ids, err := q.ReclaimStaleOperations(ctx, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != op.ID {
		t.Fatalf("reclaimed %v", ids)
	}
	got, _ := q.GetOperation(ctx, op.ID)
	if got.Status != "queued" || got.LockedBy != nil || string(got.Log) == "[]" {
		t.Fatalf("unexpected state after reclaim: %+v log=%s", got, got.Log)
	}
}
