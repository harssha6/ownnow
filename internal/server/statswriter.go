package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

const statsQueueSize = 1024
const statsActiveShards = 32
const statsShardVisitors = 1024
const statsBatchSize = 256

type statsHit struct {
	slug, day, visitor, path, referrer string
	generation                         time.Time
	file                               bool
}
type statsCommand struct {
	hit  *statsHit
	done chan error
	stop bool
}
type statsShard struct {
	hit    statsHit
	key    string
	rollup dailyRollup
	dirty  bool
}

// Generation namespaces keep a deleted slug's delayed writes on other replicas
// out of a replacement publish's stats, even without object-store transactions.
func statsPrefix(slug string, created time.Time) string {
	return "_meta/stats/" + slug + "/" + hashStr(created.UTC().Format(time.RFC3339Nano))[:32] + "/"
}
func (s *Server) startStats() {
	s.statsOnce.Do(func() { go s.runStats() })
}
func (s *Server) enqueueStats(hit statsHit) bool {
	if s.statsClosed.Load() {
		return false
	}
	s.startStats()
	select {
	case s.statsQueue <- statsCommand{hit: &hit}:
		return true
	default:
		return false // Keep public serving available under storage pressure.
	}
}
func (s *Server) syncStats(ctx context.Context, stop bool) error {
	s.startStats()
	done := make(chan error, 1)
	select {
	case <-s.statsDone:
		return nil
	case s.statsQueue <- statsCommand{done: done, stop: stop}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-s.statsDone:
		select {
		case err := <-done:
			return err
		default:
			return nil
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CloseStats drains accepted hits and flushes rollups. Call after HTTP shutdown.
func (s *Server) CloseStats(ctx context.Context) error {
	s.statsClosed.Store(true)
	return s.syncStats(ctx, true)
}
func (s *Server) flushShard(shard *statsShard) error {
	if !shard.dirty {
		return nil
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A hit carries the generation observed while serving, never a fresh lookup
	// of a reused slug. Serialize this check/write with deletion on this process.
	req := (&http.Request{}).WithContext(ctx)
	site, err := s.loadSite(req, shard.hit.slug)
	if missingObject(err) || (err == nil && !site.CreatedAt.Equal(shard.hit.generation)) {
		shard.dirty = false
		shard.rollup = newRollup()
		return nil
	}
	if err != nil {
		return err
	}
	b, err := json.Marshal(shard.rollup)
	if err != nil {
		return err
	}
	if err = s.cfg.Store.Put(ctx, shard.key, strings.NewReader(string(b)), int64(len(b)), "application/json"); err != nil {
		return err
	}
	shard.dirty = false
	return nil
}
func (s *Server) runStats() {
	defer close(s.statsDone)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	active := map[string]*statsShard{}
	pending := 0
	flush := func() error {
		var first error
		for id, shard := range active {
			if err := s.flushShard(shard); err != nil {
				if first == nil {
					first = err
				}
				// Failed writes never hold an unbounded retry backlog.
				delete(active, id)
			}
		}
		pending = 0
		if first != nil {
			log.Print("ownnow: could not persist analytics rollup")
		}
		return first
	}
	for {
		select {
		case <-ticker.C:
			_ = flush()
		case cmd := <-s.statsQueue:
			if cmd.hit == nil {
				cmd.done <- flush()
				if cmd.stop {
					return
				}
				continue
			}
			hit := *cmd.hit
			id := statsPrefix(hit.slug, hit.generation) + hit.day
			shard := active[id]
			if shard != nil && (len(shard.rollup.Visitors) >= statsShardVisitors && !shard.rollup.Visitors[hit.visitor]) {
				if err := s.flushShard(shard); err != nil {
					log.Print("ownnow: could not persist analytics rollup")
				}
				delete(active, id)
				shard = nil
			}
			if shard == nil {
				if len(active) >= statsActiveShards {
					// Seal one bounded shard. A later hit starts a new object; stats reads
					// union hashes across all shards so eviction never approximates uniques.
					for old, entry := range active {
						if err := s.flushShard(entry); err != nil {
							log.Print("ownnow: could not persist analytics rollup")
						}
						delete(active, old)
						break
					}
				}
				shard = &statsShard{hit: hit, key: id + "/" + s.statsWriter + "_" + randID("") + ".json", rollup: newRollup()}
				active[id] = shard
			}
			shard.rollup.Views++
			if hit.file {
				shard.rollup.FileOpens++
			}
			shard.rollup.Visitors[hit.visitor] = true
			boundedIncrement(shard.rollup.Paths, hit.path)
			if hit.referrer != "" {
				boundedIncrement(shard.rollup.Referrers, hit.referrer)
			}
			shard.dirty = true
			pending++
			if pending >= statsBatchSize {
				_ = flush()
			}
		}
	}
}
