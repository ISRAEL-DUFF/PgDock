package bloom

import (
	"testing"

	"github.com/google/uuid"
)

func TestBloom(t *testing.T) {
	ids := make([]uuid.UUID, 10_000)
	for i := range ids {
		ids[i] = uuid.New()
	}
	f := New(ids, 0.01)
	if len(f) > 13_000 {
		t.Fatalf("10k users at 1%%: %d bytes", len(f))
	}
	for _, id := range ids {
		if !Has(f, id) {
			t.Fatalf("a member is missing: %s", id)
		}
	}
	fp := 0
	for range 20_000 {
		if Has(f, uuid.New()) {
			fp++
		}
	}
	if rate := float64(fp) / 20_000; rate > 0.02 {
		t.Fatalf("false-positive rate %.3f", rate)
	}
	if Has(nil, ids[0]) || Has([]byte{0}, ids[0]) || Has(New(nil, 0.01), ids[0]) {
		t.Fatal("an empty filter has members")
	}
}
