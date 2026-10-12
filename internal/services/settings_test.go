package services

import (
	"errors"
	"testing"
)

func TestCacheTTLValidation(t *testing.T) {
	for name, ttl := range map[string]int{"public.products": 60, "rpc.search_products": 30, "rpc.api.search": 3600, "shop.Items": 1} {
		if err := (Settings{CacheTTLSeconds: map[string]int{name: ttl}}).Validate(); err != nil {
			t.Errorf("%s %d: %v", name, ttl, err)
		}
	}
	for name, ttl := range map[string]int{"products": 60, "public.products": 0, "rpc.search": 3601, "public.a;drop": 5, "a.b.c": 5} {
		if err := (Settings{CacheTTLSeconds: map[string]int{name: ttl}}).Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s %d accepted", name, ttl)
		}
	}
}
