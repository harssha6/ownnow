package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ownnow/internal/server"
	"ownnow/internal/store"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(server.Config{Store: st, APIKey: "test-key", PublicURL: "http://x", Secret: []byte("secret")})
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts
}

func call(t *testing.T, ts *httptest.Server, method, url string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ts.URL+url, rdr)
	req.Header.Set("Authorization", "Bearer test-key")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestPublishFlow(t *testing.T) {
	ts := testServer(t)
	code, created := call(t, ts, "POST", "/api/v1/publish", map[string]any{
		"files": []map[string]any{{"path": "index.html", "size": 11, "contentType": "text/html"}},
	}, nil)
	if code != 201 {
		t.Fatalf("create: %d %v", code, created)
	}
	slug := created["slug"].(string)
	up := created["uploads"].([]any)[0].(map[string]any)

	req, _ := http.NewRequest("PUT", ts.URL+up["url"].(string), strings.NewReader("<h1>hi</h1>"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("upload: %v %v", err, resp)
	}
	resp.Body.Close()

	// not live before finalize
	r, _ := http.Get(ts.URL + "/" + slug + "/")
	if r.StatusCode != 404 {
		t.Fatalf("pre-finalize should 404, got %d", r.StatusCode)
	}
	r.Body.Close()

	code, fin := call(t, ts, "POST", "/api/v1/publish/"+slug+"/finalize", map[string]any{"versionId": created["versionId"]}, nil)
	if code != 200 {
		t.Fatalf("finalize: %d %v", code, fin)
	}
	r, _ = http.Get(ts.URL + "/" + slug + "/")
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !strings.Contains(string(body), "hi") {
		t.Fatalf("serve: %d %s", r.StatusCode, body)
	}
}

func TestTraversalRejected(t *testing.T) {
	if _, ok := store.CleanKey("../etc/passwd"); ok {
		t.Fatal("traversal should be rejected")
	}
	if _, ok := store.CleanKey("a/b.txt"); !ok {
		t.Fatal("normal path should pass")
	}
}
