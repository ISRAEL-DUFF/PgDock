package edge

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func entry(ref, key string, body string, tags ...string) *cacheEntry {
	now := time.Now()
	return &cacheEntry{key: key, ref: ref, body: []byte(body), stored: now, expires: now.Add(time.Minute), ttl: 60, tags: tags}
}

func TestRespCacheDropsByTable(t *testing.T) {
	c := newRespCache(1 << 20)
	now := time.Now()
	c.put(entry("p1", "todos", "a", "public.todos"), 0)
	c.put(entry("p1", "lists", "b", "public.lists", "public.todos"), 0) // lists embedding todos
	c.put(entry("p1", "notes", "c", "public.notes"), 0)
	c.put(entry("p1", "fn", "d", rpcTag), 0)
	c.put(entry("p2", "todos2", "e", "public.todos"), 0)
	c.dropTables("p1", []string{"public.todos"})
	for key, want := range map[string]bool{"todos": false, "lists": false, "notes": true, "fn": false, "todos2": true} {
		if got := c.get(key, now) != nil; got != want {
			t.Errorf("%s cached %v, want %v", key, got, want)
		}
	}
	c.dropTables("p1", nil) // everything of p1
	if c.get("notes", now) != nil || c.get("todos2", now) == nil {
		t.Fatal("dropping all of p1")
	}
}

func TestRespCacheGenerationAndExpiry(t *testing.T) {
	c := newRespCache(1 << 20)
	gen := c.generation("p1")
	c.dropTables("p1", []string{"public.todos"}) // a write while the read ran
	c.put(entry("p1", "k", "stale", "public.todos"), gen)
	if c.get("k", time.Now()) != nil {
		t.Fatal("a read from before a write was stored")
	}
	c.put(entry("p1", "k", "fresh", "public.todos"), c.generation("p1"))
	if c.get("k", time.Now().Add(2*time.Minute)) != nil {
		t.Fatal("an expired entry was served")
	}
	if len(c.byKey) != 0 || len(c.byTag) != 0 || c.size != 0 {
		t.Fatalf("expired entry not removed: %d keys, %d refs, %d bytes", len(c.byKey), len(c.byTag), c.size)
	}
}

func TestRespCacheBoundedLRU(t *testing.T) {
	c := newRespCache(8000)
	body := strings.Repeat("x", 900)
	for i := 0; i < 20; i++ {
		c.put(entry("p", string(rune('a'+i)), body, "public.t"), 0)
		if i == 5 {
			c.get("a", time.Now()) // keep "a" warm
		}
	}
	if c.size > 8000 {
		t.Fatalf("size %d over the bound", c.size)
	}
	if c.get("b", time.Now()) != nil {
		t.Fatal("the least recently used entry survived")
	}
	c.put(entry("p", "huge", strings.Repeat("y", 2000), "public.t"), 0)
	if c.get("huge", time.Now()) != nil {
		t.Fatal("an entry over an eighth of the cache was stored")
	}
}

func TestCacheKeyNormalises(t *testing.T) {
	a, _ := url.ParseQuery("select=id&where=done:eq:false&apikey=pgd_pub_1&order=id:desc")
	b, _ := url.ParseQuery("order=id:desc&where=done:eq:false&select=id")
	if cacheKey("r", 1, "fp", "/data/v1/todos", a) != cacheKey("r", 1, "fp", "/data/v1/todos", b) {
		t.Fatal("parameter order or the apikey changed the key")
	}
	if cacheKey("r", 1, "fp", "/data/v1/todos", b) == cacheKey("r", 2, "fp", "/data/v1/todos", b) ||
		cacheKey("r", 1, "fp", "/data/v1/todos", b) == cacheKey("r", 1, "fp2", "/data/v1/todos", b) {
		t.Fatal("a new configuration or catalog kept the key")
	}
	if cacheName("public", "search", true) != "rpc.search" || cacheName("api", "search", true) != "rpc.api.search" ||
		cacheName("public", "products", false) != "public.products" {
		t.Fatal("cache names")
	}
}
