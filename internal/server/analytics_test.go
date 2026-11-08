package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInjectionAndSkipPresent(t *testing.T) {
	a := AnalyticsConfig{Provider: "umami", ScriptURL: "https://metrics.example/script.js", WebsiteID: "site-id"}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"<html><head></head><body>hi</body></html>", "<h1>fragment</h1>", "İ K 😀<head></head>", `<script>const text = "</scriptable><script data-website-id=x>";</script><head></head>`, "<!-- <script data-website-id=x></script> --><head></head>", `<script>const text = "<script data-website-id=x>";</script><head></head>`, `<head><meta content="a > b"></head>`, `<HEAD><TITLE>x</TITLE></HEAD>`} {
		out := string(a.inject([]byte(source)))
		if strings.Count(out, "data-ownnow-analytics") != 1 {
			t.Fatalf("no injection: %s", out)
		}
		if string(a.inject([]byte(out))) != out {
			t.Fatal("injection must be idempotent")
		}
	}
	for _, source := range []string{`<script defer data-website-id="existing" src="/custom.js"></script>`, `<SCRIPT DATA-DOMAIN='site' SRC='/tracker'></SCRIPT>`, `<script src="https://plausible.io/js/script.js"></script>`, `<script src='https://metrics.example/script.js'></script>`, `<script src='https://www.googletagmanager.com/gtag/js'></script>`} {
		if out := string(a.inject([]byte(source))); out != source {
			t.Fatalf("double tracker: %s", out)
		}
	}
	p := AnalyticsConfig{Provider: "plausible", ScriptURL: "https://metrics.example/js/script.js", Domain: "pages.example"}
	if p.Validate() != nil || !strings.Contains(string(p.inject([]byte("<head></head>"))), `data-domain="pages.example"`) {
		t.Fatal("plausible")
	}
	custom := AnalyticsConfig{Provider: "custom", Snippet: `<script src="/private-metrics.js"></script>`}
	if string(custom.inject([]byte(`<script defer src='/private-metrics.js'></script>`))) != `<script defer src='/private-metrics.js'></script>` {
		t.Fatal("custom duplicate")
	}
	for _, invalid := range []AnalyticsConfig{{Provider: "wrong"}, {Provider: "custom"}, {Provider: "umami", ScriptURL: "javascript:alert(1)", WebsiteID: "x"}, {Provider: "umami", ScriptURL: "https://example.com"}, {Provider: "plausible", ScriptURL: "https://example.com"}} {
		if invalid.Validate() == nil {
			t.Fatal(invalid)
		}
	}
}
func TestInjectionServingAndOptOut(t *testing.T) {
	s, st := statsFixture(t, func(c *Config) {
		c.Analytics = AnalyticsConfig{Provider: "umami", ScriptURL: "https://metrics.example/script.js", WebsiteID: "id"}
	})
	if w := statsRequest(s, "GET", "/report/", nil); !strings.Contains(w.Body.String(), "data-website-id") {
		t.Fatal("HTML not injected")
	}
	if w := statsRequest(s, "GET", "/report/report.pdf", nil); strings.Contains(w.Body.String(), "<script") {
		t.Fatal("PDF modified")
	}
	for _, header := range []string{"DNT", "Sec-GPC"} {
		if w := statsRequest(s, "GET", "/report/", map[string]string{header: "1"}); strings.Contains(w.Body.String(), "data-website-id") {
			t.Fatal("privacy opt-out ignored")
		}
	}
	site, err := s.loadSite(httptest.NewRequest("GET", "/", nil), "report")
	if err != nil {
		t.Fatal(err)
	}
	site.NoAnalytics = true
	if err := s.saveSite(httptest.NewRequest("GET", "/", nil), site); err != nil {
		t.Fatal(err)
	}
	if w := statsRequest(s, "GET", "/report/", nil); strings.Contains(w.Body.String(), "data-website-id") {
		t.Fatal("per-publish opt-out ignored")
	}
	rc, _, _, _ := st.Get(context.Background(), s.fileKey("report", "v1", "index.html"))
	defer rc.Close()
	// The immutable source remains untouched; injection belongs to serving.
	if got := readStats(t, s, "1").Totals.Views; got != 3 {
		t.Fatal("noAnalytics should only disable injection", got)
	}
}
