package server_test

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ownnow/internal/server"
	"ownnow/internal/store"
)

// slowStore adds a fixed delay to every metadata read and counts them, so
// tests see the cost R2 round trips have in production.
type slowStore struct {
	store.Store
	delay  time.Duration
	gets   atomic.Int64
	active atomic.Int64
	peak   atomic.Int64
}

func (s *slowStore) Get(ctx context.Context, key string) (io.ReadCloser, int64, string, error) {
	if strings.HasPrefix(key, "_meta/") {
		s.gets.Add(1)
		active := s.active.Add(1)
		defer s.active.Add(-1)
		for {
			peak := s.peak.Load()
			if active <= peak || s.peak.CompareAndSwap(peak, active) {
				break
			}
		}
		time.Sleep(s.delay)
	}
	return s.Store.Get(ctx, key)
}

func slowServer(t *testing.T, ttl time.Duration) (*httptest.Server, *slowStore) {
	t.Helper()
	local, err := store.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st := &slowStore{Store: local, delay: 20 * time.Millisecond}
	s := server.New(server.Config{Store: st, APIKey: "test-key", PublicURL: "http://x", Secret: []byte("secret"), IndexCacheTTL: ttl})
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts, st
}

func TestRobotsReadsInParallelAndCaches(t *testing.T) {
	ts, st := slowServer(t, time.Minute)
	for i := range 30 {
		extra := map[string]any{"indexable": i%3 == 0}
		publish(t, ts, fmt.Sprintf("site-%02d", i), map[string]string{"index.html": "x"}, extra)
	}
	st.gets.Store(0)
	st.peak.Store(0)
	_, robots := get(t, ts, "/robots.txt")
	// Prove concurrent metadata reads directly: elapsed-time thresholds fail
	// under CPU contention even when reads overlap correctly.
	if peak := st.peak.Load(); peak < 2 {
		t.Fatalf("cold robots.txt peak concurrent reads = %d; want at least 2", peak)
	}
	if n := st.gets.Load(); n > 40 { // 30 site JSONs + 10 password sidecars
		t.Fatalf("cold robots.txt did %d metadata reads", n)
	}
	if !strings.Contains(robots, "Sitemap: http://x/sitemap.xml") {
		t.Fatalf("robots.txt:\n%s", robots)
	}
	st.gets.Store(0)
	_, sm := get(t, ts, "/sitemap.xml")
	if reads := st.gets.Load(); reads != 0 {
		t.Fatalf("warm sitemap.xml: %d reads, want 0 reads", reads)
	}
	if got := strings.Count(sm, "<loc>"); got != 10 {
		t.Fatalf("sitemap lists %d sites, want 10:\n%s", got, sm)
	}
}

func TestIndexCacheInvalidatesOnWrites(t *testing.T) {
	ts, _ := slowServer(t, time.Hour) // long TTL: only invalidation can refresh it
	publish(t, ts, "public", map[string]string{"index.html": "x"}, map[string]any{"indexable": true})
	if _, sm := get(t, ts, "/sitemap.xml"); !strings.Contains(sm, "/public/") {
		t.Fatalf("not listed after publish:\n%s", sm)
	}
	call(t, ts, "PATCH", "/api/v1/publish/public/metadata", map[string]any{"indexable": false}, nil)
	if _, sm := get(t, ts, "/sitemap.xml"); strings.Contains(sm, "/public/") {
		t.Fatal("still listed right after PATCH indexable=false")
	}
	call(t, ts, "PATCH", "/api/v1/publish/public/metadata", map[string]any{"indexable": true}, nil)
	call(t, ts, "PATCH", "/api/v1/publish/public/metadata", map[string]any{"password": "pw"}, nil)
	if _, sm := get(t, ts, "/sitemap.xml"); strings.Contains(sm, "/public/") {
		t.Fatal("still listed after adding a password")
	}
	call(t, ts, "PATCH", "/api/v1/publish/public/metadata", map[string]any{"password": ""}, nil)
	if _, sm := get(t, ts, "/sitemap.xml"); !strings.Contains(sm, "/public/") {
		t.Fatal("not listed after removing the password")
	}
	call(t, ts, "DELETE", "/api/v1/publish/public", nil, nil)
	if _, sm := get(t, ts, "/sitemap.xml"); strings.Contains(sm, "/public/") {
		t.Fatal("still listed after delete")
	}
}

func TestIndexCacheTTLCoversOutsideWrites(t *testing.T) {
	local, _ := store.NewLocal(t.TempDir())
	cfg := server.Config{Store: local, APIKey: "test-key", PublicURL: "http://x", Secret: []byte("s"), IndexCacheTTL: 150 * time.Millisecond}
	a := httptest.NewServer(server.New(cfg))
	b := httptest.NewServer(server.New(cfg)) // a second instance on the same storage
	defer a.Close()
	defer b.Close()
	if _, sm := get(t, a, "/sitemap.xml"); strings.Contains(sm, "<loc>") {
		t.Fatal("expected an empty sitemap")
	}
	publish(t, b, "public", map[string]string{"index.html": "x"}, map[string]any{"indexable": true})
	if _, sm := get(t, a, "/sitemap.xml"); strings.Contains(sm, "/public/") {
		t.Fatal("instance a should still serve its cached list inside the TTL")
	}
	time.Sleep(200 * time.Millisecond)
	if _, sm := get(t, a, "/sitemap.xml"); !strings.Contains(sm, "/public/") {
		t.Fatal("instance a did not pick up the change after the TTL")
	}
}

func TestExpiredSiteLeavesSitemapWithoutRefresh(t *testing.T) {
	local, _ := store.NewLocal(t.TempDir())
	ts := httptest.NewServer(server.New(server.Config{Store: local, APIKey: "k", PublicURL: "http://x", Secret: []byte("s"), IndexCacheTTL: time.Hour}))
	defer ts.Close()
	exp := time.Now().UTC().Add(150 * time.Millisecond)
	meta := fmt.Sprintf(`{"slug":"brief","currentVersion":"v_1","versions":[{"id":"v_1","files":[{"path":"index.html"}]}],"indexable":true,"expiresAt":%q}`, exp.Format(time.RFC3339Nano))
	if err := local.Put(context.Background(), "_meta/sites/brief.json", strings.NewReader(meta), int64(len(meta)), "application/json"); err != nil {
		t.Fatal(err)
	}
	if _, sm := get(t, ts, "/sitemap.xml"); !strings.Contains(sm, "/brief/") {
		t.Fatalf("unexpired site missing:\n%s", sm)
	}
	time.Sleep(200 * time.Millisecond) // cache is still warm (1h TTL)
	if _, sm := get(t, ts, "/sitemap.xml"); strings.Contains(sm, "/brief/") {
		t.Fatal("expired site still listed from the cache")
	}
}
