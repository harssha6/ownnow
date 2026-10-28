package store_test

import (
	"strings"
	"testing"
	"time"

	"ownnow/internal/store"
)

func TestPresignPutShape(t *testing.T) {
	s := &store.S3{
		Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "ownnow-sites",
		Region: "auto", AccessKey: "AKID", SecretKey: "secret",
	}
	u, err := s.PresignPut("sites/demo/v_1/index.html", "text/html", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"X-Amz-Algorithm=AWS4-HMAC-SHA256", "X-Amz-Signature=", "ownnow-sites/sites/demo"} {
		if !strings.Contains(u, want) {
			t.Fatalf("presigned URL missing %q: %s", want, u)
		}
	}
}
