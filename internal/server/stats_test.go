package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"ownnow/internal/store"
)

func statsFixture(t *testing.T, change func(*Config)) (*Server, *store.Local) {
	t.Helper()
	st, err := store.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Store: st, APIKey: "test-key", Secret: []byte("stable-test-secret")}
	if change != nil {
		change(&cfg)
	}
	s := New(cfg)
	site := &Site{Slug: "report", Current: "v1", ClaimHash: hashStr("claim-report"), Versions: []Version{{ID: "v1", Files: []FileEntry{{Path: "index.html", ContentType: "text/html"}, {Path: "report.pdf", ContentType: "application/pdf"}, {Path: "image.png", ContentType: "image/png"}}}}}
	if err := s.saveSite(httptest.NewRequest("GET", "/", nil), site); err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{"index.html": "<!doctype html><html><head><title>Report</title></head><body>Hi</body></html>", "report.pdf": "%PDF-1.4\nTest PDF", "image.png": "PNG"} {
		if err := st.Put(context.Background(), s.fileKey("report", "v1", p), strings.NewReader(body), int64(len(body)), ""); err != nil {
			t.Fatal(err)
		}
	}
	return s, st
}
func statsRequest(s *Server, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "203.0.113.10:1234"
	r.Header.Set("User-Agent", "Mozilla/5.0 Firefox/140.0")
	r.Header.Set("Accept", "text/html,application/pdf,*/*")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func readStats(t *testing.T, s *Server, days string) StatsResponse {
	t.Helper()
	w := statsRequest(s, "GET", "/api/v1/publishes/report/stats?days="+days, map[string]string{"Authorization": "Bearer test-key"})
	if w.Code != 200 {
		t.Fatalf("stats: %d %s", w.Code, w.Body)
	}
	var out StatsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestCountsHTMLPDFAndImages(t *testing.T) {
	s, st := statsFixture(t, nil)
	for _, p := range []string{"/report/?secret=query", "/report/report.pdf", "/report/image.png"} {
		w := statsRequest(s, "GET", p, map[string]string{"Referer": "https://news.example:8443/private/path?token=hidden#fragment"})
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		if w.Header().Get("Cache-Control") != "private, no-cache" {
			t.Fatal("counting must revalidate through the server")
		}
	}
	statsRequest(s, "GET", "/report/", map[string]string{"User-Agent": "Mozilla/5.0 Safari/other"})
	out := readStats(t, s, "2")
	if out.Totals != (StatsCounts{Views: 4, UniqueVisitors: 2, FileOpens: 2}) {
		t.Fatalf("counts: %+v", out.Totals)
	}
	if len(out.Series) != 2 || out.Series[0].Views != 0 || out.Series[1].Views != 4 || out.Timezone != "UTC" {
		t.Fatalf("series: %+v", out)
	}
	if len(out.TopReferrers) != 1 || out.TopReferrers[0] != (StatsReferrer{"news.example", 3}) {
		t.Fatalf("refs: %+v", out.TopReferrers)
	}
	if len(out.TopPaths) != 3 || out.TopPaths[0] != (StatsPath{"/", 2}) {
		t.Fatalf("paths: %+v", out.TopPaths)
	}
	objs, err := st.List(context.Background(), "_meta/stats/")
	if err != nil || len(objs) != 1 {
		t.Fatalf("one rollup: %v %v", objs, err)
	}
	rc, _, _, _ := st.Get(context.Background(), objs[0].Key)
	data, _ := io.ReadAll(rc)
	rc.Close()
	for _, private := range []string{"203.0.113.10", "Mozilla", "private/path", "hidden", "secret=query"} {
		if bytes.Contains(data, []byte(private)) {
			t.Fatalf("stored private request detail %q", private)
		}
	}
}
func TestFilteringAndStatus(t *testing.T) {
	s, _ := statsFixture(t, nil)
	for _, headers := range []map[string]string{
		{"User-Agent": "Googlebot/2.1"}, {"User-Agent": "facebookexternalhit/1"}, {"User-Agent": ""}, {"User-Agent": "curl/8.0"}, {"Accept": ""}, {"DNT": "1"}, {"Sec-GPC": "1"}, {"Sec-Fetch-Dest": "image"}, {"Sec-Fetch-Dest": "script"},
	} {
		statsRequest(s, "GET", "/report/", headers)
	}
	statsRequest(s, "HEAD", "/report/", nil)
	statsRequest(s, "GET", "/report/missing", nil)
	statsRequest(s, "GET", "/report/report.pdf", map[string]string{"Range": "bytes=9999-"})
	if got := readStats(t, s, "1").Totals.Views; got != 0 {
		t.Fatal(got)
	}
	w := statsRequest(s, "GET", "/report/report.pdf", map[string]string{"Range": "bytes=0-3"})
	if w.Code != 206 {
		t.Fatal(w.Code)
	}
	first := statsRequest(s, "GET", "/report/", nil)
	second := statsRequest(s, "GET", "/report/", map[string]string{"If-None-Match": first.Header().Get("ETag")})
	if second.Code != 304 {
		t.Fatal(second.Code)
	}
	if got := readStats(t, s, "1").Totals; got != (StatsCounts{Views: 3, FileOpens: 1, UniqueVisitors: 1}) {
		t.Fatal(got)
	}
}
func TestDailyRollupsAndRestartMerge(t *testing.T) {
	s, st := statsFixture(t, nil)
	today := time.Now().UTC().Format(time.DateOnly)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	a := s.visitorHash(today, "report", "203.0.113.5", "Browser")
	b := s.visitorHash(yesterday, "report", "203.0.113.5", "Browser")
	if a == b || a == s.visitorHash(today, "different", "203.0.113.5", "Browser") {
		t.Fatal("hash must rotate per day and publish")
	}
	if err := s.record(context.Background(), "report", yesterday, b, "/", "", false); err != nil {
		t.Fatal(err)
	}
	if err := s.record(context.Background(), "report", today, a, "/", "", false); err != nil {
		t.Fatal(err)
	}
	restarted := New(Config{Store: st, APIKey: "test-key", Secret: s.cfg.Secret})
	if err := restarted.record(context.Background(), "report", today, a, "/file.pdf", "example.com", true); err != nil {
		t.Fatal(err)
	}
	got := readStats(t, restarted, "2")
	if got.Totals != (StatsCounts{Views: 3, UniqueVisitors: 2, FileOpens: 1}) || got.Series[1].UniqueVisitors != 1 {
		t.Fatalf("merge: %+v", got)
	}
}
func TestConcurrentHits(t *testing.T) {
	s, _ := statsFixture(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); statsRequest(s, "GET", "/report/", nil) }()
	}
	wg.Wait()
	if got := readStats(t, s, "1").Totals; got.Views != 25 || got.UniqueVisitors != 1 {
		t.Fatal(got)
	}
}
func TestStatsAuthAndDays(t *testing.T) {
	s, _ := statsFixture(t, nil)
	for _, tc := range []struct {
		headers map[string]string
		code    int
	}{
		{nil, 401}, {map[string]string{"Authorization": "Bearer wrong"}, 403},
		{map[string]string{"X-OwnNow-Claim": "claim-report"}, 200},
		{map[string]string{"Authorization": "Bearer claim-report"}, 200},
		{map[string]string{"Authorization": "Bearer test-key"}, 200},
	} {
		w := statsRequest(s, "GET", "/api/v1/publishes/report/stats", tc.headers)
		if w.Code != tc.code {
			t.Fatalf("%v: %d", tc.headers, w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("stats cache")
		}
	}
	for _, days := range []string{"0", "-1", "366", "x"} {
		if w := statsRequest(s, "GET", "/api/v1/publishes/report/stats?days="+days, map[string]string{"Authorization": "Bearer test-key"}); w.Code != 400 {
			t.Fatalf("%s: %d", days, w.Code)
		}
	}
	local, _ := statsFixture(t, func(c *Config) { c.APIKey = "" })
	if w := statsRequest(local, "GET", "/api/v1/publishes/report/stats", nil); w.Code != 401 {
		t.Fatal("local-dev stats must require credentials")
	}
}
func TestDisabledAndDelete(t *testing.T) {
	s, st := statsFixture(t, func(c *Config) { c.DisableCounting = true })
	statsRequest(s, "GET", "/report/", nil)
	if out := readStats(t, s, "1"); out.CountingEnabled || out.Totals.Views != 0 {
		t.Fatal(out)
	}
	s.cfg.DisableCounting = false
	statsRequest(s, "GET", "/report/", nil)
	w := statsRequest(s, "DELETE", "/api/v1/publish/report", map[string]string{"Authorization": "Bearer test-key"})
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	objs, _ := st.List(context.Background(), "_meta/stats/report/")
	if len(objs) != 0 {
		t.Fatal("delete must remove analytics")
	}
}
func TestTrustedProxyIP(t *testing.T) {
	s, _ := statsFixture(t, nil)
	r := httptest.NewRequest("GET", "/report/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.2, 10.0.0.2")
	if ip := s.clientIP(r); ip != "127.0.0.1" {
		t.Fatal(ip)
	}
	s.cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("10.0.0.0/8")}
	if ip := s.clientIP(r); ip != "198.51.100.2" {
		t.Fatal(ip)
	}
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 203.0.113.2")
	if ip := s.clientIP(r); ip != "203.0.113.2" {
		t.Fatal("must stop at untrusted hop", ip)
	}
}
func TestEdgeIngest(t *testing.T) {
	s, _ := statsFixture(t, func(c *Config) { c.EdgeStatsKey = "edge-key" })
	hit := edgeHit{Date: time.Now().UTC().Format(time.DateOnly), VisitorHash: strings.Repeat("ab", 32), Path: "/report.pdf", Referrer: "example.com", File: true}
	send := func(hit edgeHit, key string) int {
		b, _ := json.Marshal(hit)
		r := httptest.NewRequest("POST", "/api/v1/publishes/report/stats/edge", bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	if send(hit, "test-key") != 401 || send(hit, "edge-key") != 204 {
		t.Fatal("edge auth")
	}
	bad := hit
	bad.Path = "/report.pdf?private=1"
	if send(bad, "edge-key") != 400 {
		t.Fatal("query must be rejected")
	}
	bad = hit
	bad.Referrer = "https://example.com/private"
	if send(bad, "edge-key") != 400 {
		t.Fatal("full referrer must be rejected")
	}
	if got := readStats(t, s, "1").Totals; got != (StatsCounts{Views: 1, UniqueVisitors: 1, FileOpens: 1}) {
		t.Fatal(got)
	}
}
