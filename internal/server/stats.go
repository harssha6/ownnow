package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// StatsCounts.Views includes page views and file opens. FileOpens is a subset.
type StatsCounts struct {
	Views          int64 `json:"views"`
	UniqueVisitors int64 `json:"uniqueVisitors"`
	FileOpens      int64 `json:"fileOpens"`
}
type StatsDay struct {
	Date string `json:"date"`
	StatsCounts
}
type StatsReferrer struct {
	Host  string `json:"host"`
	Views int64  `json:"views"`
}
type StatsPath struct {
	Path  string `json:"path"`
	Views int64  `json:"views"`
}
type StatsResponse struct {
	Slug            string          `json:"slug"`
	Days            int             `json:"days"`
	Timezone        string          `json:"timezone"`
	CountingEnabled bool            `json:"countingEnabled"`
	Totals          StatsCounts     `json:"totals"`
	Series          []StatsDay      `json:"series"`
	TopReferrers    []StatsReferrer `json:"topReferrers"`
	TopPaths        []StatsPath     `json:"topPaths"`
}

// Each process writes its own daily shard, avoiding lost S3 updates between
// servers. Readers merge the shards and union the daily visitor hashes.
// No request timestamps, addresses, user agents or complete referrers are stored.
type dailyRollup struct {
	Views     int64            `json:"views"`
	FileOpens int64            `json:"fileOpens"`
	Visitors  map[string]bool  `json:"visitors"`
	Referrers map[string]int64 `json:"referrers"`
	Paths     map[string]int64 `json:"paths"`
}

func newRollup() dailyRollup {
	return dailyRollup{Visitors: map[string]bool{}, Referrers: map[string]int64{}, Paths: map[string]int64{}}
}
func missingObject(err error) bool { return errors.Is(err, io.EOF) || errors.Is(err, os.ErrNotExist) }
func (s *Server) readRollup(ctx context.Context, key string) (dailyRollup, error) {
	d := newRollup()
	rc, _, _, err := s.cfg.Store.Get(ctx, key)
	if missingObject(err) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	defer rc.Close()
	err = json.NewDecoder(rc).Decode(&d)
	if d.Visitors == nil {
		d.Visitors = map[string]bool{}
	}
	if d.Referrers == nil {
		d.Referrers = map[string]int64{}
	}
	if d.Paths == nil {
		d.Paths = map[string]int64{}
	}
	return d, err
}
func (s *Server) visitorHash(day, slug, ip, ua string) string {
	salt := hmac.New(sha256.New, s.cfg.Secret)
	salt.Write([]byte("ownnow-stats\x00" + day + "\x00" + slug))
	h := hmac.New(sha256.New, salt.Sum(nil))
	h.Write([]byte(ip + "\x00" + ua))
	return hex.EncodeToString(h.Sum(nil))
}
func countable(r *http.Request) bool {
	if r.Method != http.MethodGet || r.Header.Get("DNT") == "1" || r.Header.Get("Sec-GPC") == "1" || strings.TrimSpace(r.Header.Get("Accept")) == "" {
		return false
	}
	ua := strings.ToLower(r.UserAgent())
	if ua == "" {
		return false
	}
	for _, bot := range []string{"bot", "spider", "crawler", "slurp", "headless", "facebookexternalhit", "preview", "lighthouse", "wget", "curl/", "python-requests", "go-http-client", "uptime", "monitor", "scanner", "scrapy"} {
		if strings.Contains(ua, bot) {
			return false
		}
	}
	// Browser asset fetches aren't visits. Direct PDF/image/download navigation
	// has destination=document (or no Fetch Metadata, e.g. a CLI download).
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "", "empty":
		mode := r.Header.Get("Sec-Fetch-Mode")
		return mode == "" || mode == "navigate"
	case "document", "iframe":
		return true
	default:
		return false
	}
}
func referrerHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Hostname() == "" || len(u.Hostname()) > 253) {
		return ""
	}
	return strings.Clone(strings.ToLower(u.Hostname()))
}
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	peer = peer.Unmap()
	trusted := func(ip netip.Addr) bool {
		for _, prefix := range s.cfg.TrustedProxies {
			if prefix.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !trusted(peer) && r.Header.Get("X-Forwarded-For") != "" {
		s.proxyWarning.Do(func() {
			log.Print("ownnow: forwarded client IP ignored; configure OWNNOW_TRUSTED_PROXIES for accurate daily uniques behind your proxy")
		})
	}
	// Ignore spoofable forwarding headers unless the immediate peer is trusted.
	if trusted(peer) {
		chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(chain) - 1; i >= 0; i-- {
			ip, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
			if err != nil {
				return peer.String()
			}
			peer = ip.Unmap()
			if !trusted(peer) {
				break
			}
		}
	}
	return peer.String()
}
func boundedIncrement(m map[string]int64, key string) {
	if len(key) > 2048 {
		key = "(other)"
	}
	if _, ok := m[key]; !ok && len(m) >= 1000 {
		key = "(other)"
	}
	m[key]++
}
func (s *Server) countRequest(r *http.Request, site *Site, file bool) {
	if s.cfg.DisableCounting || !countable(r) {
		return
	}
	day := time.Now().UTC().Format(time.DateOnly)
	_, rest := s.resolveSlug(r)
	if len(rest) > 2047 {
		rest = "(other)"
	}
	s.enqueueStats(statsHit{slug: site.Slug, generation: site.CreatedAt, day: day, visitor: s.visitorHash(day, site.Slug, s.clientIP(r), r.UserAgent()), path: "/" + rest, referrer: referrerHost(r.Referer()), file: file})
}

// Preserve normal caching for web assets; documents/images/downloads revalidate.
func revalidateForStats(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if strings.HasPrefix(ct, "font/") {
		return false
	}
	switch ct {
	case "text/css", "text/javascript", "application/javascript", "application/json", "application/wasm", "application/font-woff", "application/vnd.ms-fontobject":
		return false
	}
	return true
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Require a credential even when local-dev publishing is unauthenticated.
	claim := r.Header.Get("X-OwnNow-Claim")
	if claim == "" {
		claim = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if !s.authed(r) && claim == "" {
		fail(w, 401, "API key or claim token required")
		return
	}
	slug := r.PathValue("slug")
	if !validSlug(slug) {
		fail(w, 400, "invalid slug")
		return
	}
	site, err := s.loadSite(r, slug)
	if err != nil {
		fail(w, 404, "site not found")
		return
	}
	if !s.authed(r) && (site.ClaimHash == "" || !hmac.Equal([]byte(hashStr(claim)), []byte(site.ClaimHash))) {
		fail(w, 403, "forbidden")
		return
	}
	days := 7
	if raw := r.URL.Query().Get("days"); raw != "" {
		days, err = strconv.Atoi(raw)
		if err != nil || days < 1 || days > 365 {
			fail(w, 400, "days must be between 1 and 365")
			return
		}
	}
	out := StatsResponse{Slug: slug, Days: days, Timezone: "UTC", CountingEnabled: !s.cfg.DisableCounting, Series: []StatsDay{}, TopReferrers: []StatsReferrer{}, TopPaths: []StatsPath{}}
	refs, paths := map[string]int64{}, map[string]int64{}
	if err := s.syncStats(r.Context(), false); err != nil {
		fail(w, 500, "could not flush stats")
		return
	}
	now := time.Now().UTC()
	// Serialize against this writer so local disk readers never race a write.
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	// Reauthorize after draining the queue: deletion/recreation can change claims.
	site, err = s.loadSite(r, slug)
	if err != nil {
		fail(w, 404, "site not found")
		return
	}
	if !s.authed(r) && (site.ClaimHash == "" || !hmac.Equal([]byte(hashStr(claim)), []byte(site.ClaimHash))) {
		fail(w, 403, "forbidden")
		return
	}
	prefix := statsPrefix(slug, site.CreatedAt)
	objects, err := s.cfg.Store.List(r.Context(), prefix)
	if err != nil {
		fail(w, 500, "could not read stats")
		return
	}
	byDay := map[string][]string{}
	for _, obj := range objects {
		parts := strings.Split(strings.TrimPrefix(obj.Key, prefix), "/")
		if len(parts) == 2 && strings.HasSuffix(parts[1], ".json") {
			byDay[parts[0]] = append(byDay[parts[0]], obj.Key)
		}
	}
	for i := days - 1; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format(time.DateOnly)
		d := StatsDay{Date: day}
		visitors := map[string]bool{}
		for _, key := range byDay[day] {
			roll, err := s.readRollup(r.Context(), key)
			if err != nil {
				fail(w, 500, "could not read stats")
				return
			}
			d.Views += roll.Views
			d.FileOpens += roll.FileOpens
			for v := range roll.Visitors {
				visitors[v] = true
			}
			for k, v := range roll.Referrers {
				refs[k] += v
			}
			for k, v := range roll.Paths {
				paths[k] += v
			}
		}
		d.UniqueVisitors = int64(len(visitors))
		out.Series = append(out.Series, d)
		out.Totals.Views += d.Views
		out.Totals.FileOpens += d.FileOpens
		out.Totals.UniqueVisitors += d.UniqueVisitors
	}
	for k, v := range refs {
		out.TopReferrers = append(out.TopReferrers, StatsReferrer{k, v})
	}
	for k, v := range paths {
		out.TopPaths = append(out.TopPaths, StatsPath{k, v})
	}
	sort.Slice(out.TopReferrers, func(i, j int) bool {
		a, b := out.TopReferrers[i], out.TopReferrers[j]
		if a.Views == b.Views {
			return a.Host < b.Host
		}
		return a.Views > b.Views
	})
	sort.Slice(out.TopPaths, func(i, j int) bool {
		a, b := out.TopPaths[i], out.TopPaths[j]
		if a.Views == b.Views {
			return a.Path < b.Path
		}
		return a.Views > b.Views
	})
	if len(out.TopReferrers) > 20 {
		out.TopReferrers = out.TopReferrers[:20]
	}
	if len(out.TopPaths) > 20 {
		out.TopPaths = out.TopPaths[:20]
	}
	writeJSON(w, 200, out)
}

// Edge requests carry only the daily hash and sanitized dimensions, never IP/UA.
// A separate shared key limits this endpoint to a trusted serving Worker.
type edgeHit struct {
	Date          string    `json:"date"`
	VisitorHash   string    `json:"visitorHash"`
	Path          string    `json:"path"`
	Referrer      string    `json:"referrer"`
	File          bool      `json:"file"`
	SiteCreatedAt time.Time `json:"siteCreatedAt"`
}

func (s *Server) edgeStats(w http.ResponseWriter, r *http.Request) {
	if s.cfg.EdgeStatsKey == "" || !hmac.Equal([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.cfg.EdgeStatsKey)) {
		fail(w, 401, "edge stats key required")
		return
	}
	if s.cfg.DisableCounting {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var hit edgeHit
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	err := dec.Decode(&hit)
	hash, hashErr := hex.DecodeString(hit.VisitorHash)
	today := time.Now().UTC().Format(time.DateOnly)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	if err != nil || hashErr != nil || len(hash) != 32 || (hit.Date != today && hit.Date != yesterday) || len(hit.Path) > 2048 || !strings.HasPrefix(hit.Path, "/") || strings.ContainsAny(hit.Path, "?#") || len(hit.Referrer) > 253 || strings.ContainsAny(hit.Referrer, "/:?#@\\") {
		fail(w, 400, "invalid edge stats")
		return
	}
	slug := r.PathValue("slug")
	if !validSlug(slug) {
		fail(w, 400, "invalid slug")
		return
	}
	site, err := s.loadSite(r, slug)
	if err != nil || site.Current == "" {
		fail(w, 404, "site not found")
		return
	}
	if !site.CreatedAt.Equal(hit.SiteCreatedAt) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.enqueueStats(statsHit{slug: slug, generation: hit.SiteCreatedAt, day: hit.Date, visitor: strings.ToLower(hit.VisitorHash), path: hit.Path, referrer: hit.Referrer, file: hit.File})
	w.WriteHeader(http.StatusNoContent)
}

type statsStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statsStatusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func privacyRequested(r *http.Request) bool {
	return r.Header.Get("DNT") == "1" || r.Header.Get("Sec-GPC") == "1"
}
