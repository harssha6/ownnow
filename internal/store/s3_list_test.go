package store

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestS3ListPaginatesRollupShards(t *testing.T) {
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("prefix") != "_meta/stats/report/" || r.Header.Get("Authorization") == "" {
			t.Error("prefix/signature missing")
		}
		if r.URL.Query().Get("continuation-token") == "" {
			fmt.Fprint(w, `<ListBucketResult><Contents><Key>_meta/stats/report/2026-11-08/b.json</Key><Size>2</Size></Contents><IsTruncated>true</IsTruncated><NextContinuationToken>next+page</NextContinuationToken></ListBucketResult>`)
		} else {
			if r.URL.Query().Get("continuation-token") != "next+page" {
				t.Error("continuation token not preserved")
			}
			fmt.Fprint(w, `<ListBucketResult><Contents><Key>_meta/stats/report/2026-11-08/a.json</Key><Size>1</Size></Contents><IsTruncated>false</IsTruncated></ListBucketResult>`)
		}
	}))
	defer ts.Close()
	s := S3{Endpoint: ts.URL, Bucket: "test", Region: "auto", AccessKey: "test", SecretKey: "test"}
	objects, err := s.List(context.Background(), "_meta/stats/report/")
	if err != nil || requests != 2 || len(objects) != 2 || objects[0].Size != 1 || objects[1].Size != 2 {
		t.Fatalf("%+v, %v (%d requests)", objects, err, requests)
	}
}
func TestS3ListDoesNotHideErrors(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `<ListBucketResult><IsTruncated>true</IsTruncated></ListBucketResult>`)
			}))
			defer ts.Close()
			s := S3{Endpoint: ts.URL, Bucket: "test", Region: "auto"}
			if _, err := s.List(context.Background(), "_meta/stats/"); err == nil {
				t.Fatal("listing failure must not become zero stats")
			}
		})
	}
}
