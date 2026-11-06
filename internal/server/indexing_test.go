package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// publish creates, uploads and finalizes a site with the given extra fields.
func publish(t *testing.T, ts *httptest.Server, slug string, files map[string]string, extra map[string]any) {
	t.Helper()
	var metas []map[string]any
	for p, body := range files {
		metas = append(metas, map[string]any{"path": p, "size": len(body)})
	}
	req := map[string]any{"files": metas}
	for k, v := range extra {
		req[k] = v
	}
	code, created := call(t, ts, "PUT", "/api/v1/publish/"+slug, req, nil)
	if code != 201 {
		t.Fatalf("create %s: %d %v", slug, code, created)
	}
	for _, u := range created["uploads"].([]any) {
		up := u.(map[string]any)
		r, _ := http.NewRequest("PUT", ts.URL+up["url"].(string), strings.NewReader(files[up["path"].(string)]))
		resp, err := http.DefaultClient.Do(r)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("upload: %v %v", err, resp)
		}
		resp.Body.Close()
	}
	if code, fin := call(t, ts, "POST", "/api/v1/publish/"+slug+"/finalize", map[string]any{"versionId": created["versionId"]}, nil); code != 200 {
		t.Fatalf("finalize: %d %v", code, fin)
	}
}

func get(t *testing.T, ts *httptest.Server, p string) (*http.Response, string) {
	t.Helper()
	r, err := http.Get(ts.URL + p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return r, string(b)
}

func indexableOf(t *testing.T, ts *httptest.Server, slug string) any {
	t.Helper()
	_, out := call(t, ts, "GET", "/api/v1/publish/"+slug, nil, nil)
	return out["site"].(map[string]any)["indexable"]
}

func TestNoindexHeaderOnEveryResponse(t *testing.T) {
	ts := testServer(t)
	publish(t, ts, "private", map[string]string{"index.html": "<h1>p</h1>", "doc.pdf": "%PDF-1.4"}, map[string]any{"indexable": false})
	for _, p := range []string{"/private/", "/private/doc.pdf", "/private/missing"} {
		r, _ := get(t, ts, p)
		if got := r.Header.Get("X-Robots-Tag"); got != "noindex, nofollow, noarchive" {
			t.Errorf("%s: X-Robots-Tag = %q", p, got)
		}
	}
	// the password page is the one response a crawler sees for a locked site
	call(t, ts, "PATCH", "/api/v1/publish/private/metadata", map[string]any{"password": "pw"}, nil)
	if r, _ := get(t, ts, "/private/"); r.StatusCode != 401 || r.Header.Get("X-Robots-Tag") == "" {
		t.Errorf("locked page: %d %q", r.StatusCode, r.Header.Get("X-Robots-Tag"))
	}
}

func TestUnsetAndIndexableSendNoHeader(t *testing.T) {
	ts := testServer(t)
	publish(t, ts, "legacy", map[string]string{"index.html": "x"}, nil)
	publish(t, ts, "public", map[string]string{"index.html": "x"}, map[string]any{"indexable": true})
	for _, p := range []string{"/legacy/", "/public/"} {
		if r, _ := get(t, ts, p); r.Header.Get("X-Robots-Tag") != "" {
			t.Errorf("%s should carry no X-Robots-Tag, got %q", p, r.Header.Get("X-Robots-Tag"))
		}
	}
	if v := indexableOf(t, ts, "legacy"); v != nil {
		t.Errorf("unset site reports indexable=%v", v)
	}
}

func TestIndexableRoundTrip(t *testing.T) {
	ts := testServer(t)
	publish(t, ts, "flip", map[string]string{"index.html": "x"}, map[string]any{"indexable": false})
	if v := indexableOf(t, ts, "flip"); v != false {
		t.Fatalf("after publish: %v", v)
	}
	// republish without the field keeps the stored value
	publish(t, ts, "flip", map[string]string{"index.html": "y"}, nil)
	if v := indexableOf(t, ts, "flip"); v != false {
		t.Fatalf("after republish: %v", v)
	}
	call(t, ts, "PATCH", "/api/v1/publish/flip/metadata", map[string]any{"indexable": true}, nil)
	if v := indexableOf(t, ts, "flip"); v != true {
		t.Fatalf("after PATCH true: %v", v)
	}
	if r, _ := get(t, ts, "/flip/"); r.Header.Get("X-Robots-Tag") != "" {
		t.Fatal("header still sent after flipping to indexable")
	}
	// a PATCH that does not mention indexable leaves it alone
	call(t, ts, "PATCH", "/api/v1/publish/flip/metadata", map[string]any{"displayName": "n"}, nil)
	if v := indexableOf(t, ts, "flip"); v != true {
		t.Fatalf("unrelated PATCH changed it: %v", v)
	}
	call(t, ts, "PATCH", "/api/v1/publish/flip/metadata", map[string]any{"indexable": nil}, nil)
	if v := indexableOf(t, ts, "flip"); v != nil {
		t.Fatalf("PATCH null should unset: %v", v)
	}
	if code, _ := call(t, ts, "PATCH", "/api/v1/publish/flip/metadata", map[string]any{"indexable": "yes"}, nil); code != 400 {
		t.Fatalf("bad value: %d", code)
	}
	_, list := call(t, ts, "GET", "/api/v1/publishes", nil, nil)
	if _, ok := list["sites"].([]any)[0].(map[string]any)["indexable"]; ok {
		t.Fatal("list should omit indexable once unset")
	}
}

func TestRobotsAndSitemapNameOnlyIndexableSites(t *testing.T) {
	ts := testServer(t)
	publish(t, ts, "public", map[string]string{"index.html": "x", "sitemap.xml": "<urlset/>"}, map[string]any{"indexable": true})
	publish(t, ts, "private", map[string]string{"index.html": "x"}, map[string]any{"indexable": false})
	publish(t, ts, "legacy", map[string]string{"index.html": "x"}, nil)
	publish(t, ts, "locked", map[string]string{"index.html": "x"}, map[string]any{"indexable": true, "password": "pw"})

	r, robots := get(t, ts, "/robots.txt")
	if r.StatusCode != 200 || !strings.Contains(robots, "Allow: /") || !strings.Contains(robots, "Sitemap: http://x/sitemap.xml") ||
		!strings.Contains(robots, "Sitemap: http://x/public/sitemap.xml") {
		t.Fatalf("robots.txt:\n%s", robots)
	}
	if strings.Contains(robots, "Disallow") {
		t.Fatal("robots.txt must not disallow anything (it would hide the noindex header)")
	}
	_, sm := get(t, ts, "/sitemap.xml")
	if !strings.Contains(sm, "<loc>http://x/public/</loc>") {
		t.Fatalf("sitemap missing public site:\n%s", sm)
	}
	for _, leak := range []string{"private", "legacy", "locked"} {
		if strings.Contains(robots+sm, leak) {
			t.Errorf("%q leaked into robots.txt or sitemap.xml", leak)
		}
	}
}
