package server

import (
	"context"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultIndexCacheTTL bounds how stale robots.txt and sitemap.xml can be
// after a change this process did not make itself (another instance, a direct
// storage edit). Changes made through this server's API clear the cache at once.
const DefaultIndexCacheTTL = 60 * time.Second

// indexFetchWorkers caps parallel metadata reads so a large store does not
// open hundreds of storage connections at once.
const indexFetchWorkers = 8

// indexCache holds the sites that opted in to search. Building it means one
// List plus a metadata read per site, which on R2 costs a round trip each.
type indexCache struct {
	refresh sync.Mutex // one rebuild at a time; concurrent callers wait for it
	mu      sync.Mutex
	at      time.Time
	sites   []*Site
	valid   bool
	gen     atomic.Uint64 // bumped on every invalidation
}

func (c *indexCache) invalidate() {
	c.gen.Add(1)
	c.mu.Lock()
	c.valid = false
	c.mu.Unlock()
}

func (c *indexCache) fresh(ttl time.Duration) ([]*Site, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.valid && time.Since(c.at) < ttl {
		return c.sites, true
	}
	return nil, false
}

func (s *Server) indexTTL() time.Duration {
	if s.cfg.IndexCacheTTL == 0 {
		return DefaultIndexCacheTTL
	}
	return s.cfg.IndexCacheTTL
}

// indexCandidates returns sites with indexable=true, a live version and no
// password. Expiry is checked by the caller on every read, so a cached entry
// never outlives its site's TTL.
func (s *Server) indexCandidates(ctx context.Context) []*Site {
	ttl := s.indexTTL()
	if ttl > 0 {
		if sites, ok := s.idx.fresh(ttl); ok {
			return sites
		}
	}
	s.idx.refresh.Lock()
	defer s.idx.refresh.Unlock()
	if ttl > 0 {
		if sites, ok := s.idx.fresh(ttl); ok { // another caller just rebuilt it
			return sites
		}
	}
	gen := s.idx.gen.Load()
	sites := s.loadCandidates(ctx)
	if ttl > 0 && s.idx.gen.Load() == gen { // nothing changed while we read
		s.idx.mu.Lock()
		s.idx.sites, s.idx.at, s.idx.valid = sites, time.Now(), true
		s.idx.mu.Unlock()
	}
	return sites
}

func (s *Server) loadCandidates(ctx context.Context) []*Site {
	objs, _ := s.cfg.Store.List(ctx, "_meta/sites/")
	slugs := make(chan string)
	var mu sync.Mutex
	var out []*Site
	var wg sync.WaitGroup
	for range min(indexFetchWorkers, len(objs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for slug := range slugs {
				if st := s.loadCandidate(ctx, slug); st != nil {
					mu.Lock()
					out = append(out, st)
					mu.Unlock()
				}
			}
		}()
	}
	for _, o := range objs {
		slugs <- strings.TrimSuffix(path.Base(o.Key), ".json")
	}
	close(slugs)
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// loadCandidate reads the site JSON, and the password sidecar only for sites
// that opted in, so most sites cost a single read.
func (s *Server) loadCandidate(ctx context.Context, slug string) *Site {
	rc, _, _, err := s.cfg.Store.Get(ctx, s.metaKey(slug))
	if err != nil {
		return nil
	}
	var st Site
	err = json.NewDecoder(rc).Decode(&st)
	rc.Close()
	if err != nil || st.Indexable == nil || !*st.Indexable || st.Current == "" {
		return nil
	}
	if rc2, _, _, err := s.cfg.Store.Get(ctx, "_meta/secrets/"+slug+".json"); err == nil {
		var sec struct{ PasswordHash string }
		_ = json.NewDecoder(rc2).Decode(&sec)
		rc2.Close()
		if sec.PasswordHash != "" {
			return nil
		}
	}
	return &st
}
