package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"ownnow/internal/store"
	"testing"
)

func TestStoredSecretSurvivesRestart(t *testing.T) {
	st, err := store.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := storedSecret(st)
	if err != nil {
		t.Fatal(err)
	}
	second, err := storedSecret(st)
	if err != nil || len(first) != 32 || !bytes.Equal(first, second) {
		t.Fatal("secret changed", err)
	}
}
func TestStatsCLI(t *testing.T) {
	var requested string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.RequestURI()
		if r.Header.Get("Authorization") != "Bearer cli-key" {
			t.Error("missing key")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"slug":"report","days":3,"timezone":"UTC","countingEnabled":true,"totals":{"views":2,"uniqueVisitors":1,"fileOpens":1},"series":[],"topReferrers":[],"topPaths":[]}`))
	}))
	defer ts.Close()
	t.Setenv("OWNNOW_SERVER", ts.URL)
	t.Setenv("OWNNOW_API_KEY", "cli-key")
	if err := stats([]string{"report", "--days", "3"}); err != nil {
		t.Fatal(err)
	}
	if requested != "/api/v1/publishes/report/stats?days=3" {
		t.Fatal(requested)
	}
	for _, args := range [][]string{{}, {"report", "--days"}, {"report", "--days", "0"}, {"report", "--days", "x"}, {"../bad"}, {"bad\nslug"}, {"report", "other"}} {
		if err := stats(args); err == nil {
			t.Fatal("bad args accepted", args)
		}
	}
}
