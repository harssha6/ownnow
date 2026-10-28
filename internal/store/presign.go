package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Presigner lets publish uploads/downloads bypass our server entirely:
// the client PUTs straight to R2 with a short-lived SigV4 query-signed URL.
// This is what makes the Vercel deployment work (Functions cap request
// bodies at 4.5 MB) and it matches here.now's presigned-upload design.
type Presigner interface {
	PresignPut(key, contentType string, expiry time.Duration) (string, error)
	PresignGet(key string, expiry time.Duration) (string, error)
}

// presign builds a SigV4 query-signed URL (UNSIGNED-PAYLOAD).
func (s *S3) presign(method, key string, expiry time.Duration) (string, error) {
	now := time.Now().UTC()
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", date, s.Region)

	q := url.Values{}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", s.AccessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", fmt.Sprintf("%d", int(expiry.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")

	u, err := url.Parse(s.url(key))
	if err != nil {
		return "", err
	}
	// canonical query: sorted by key
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var canonQ []string
	for _, k := range keys {
		canonQ = append(canonQ, url.QueryEscape(k)+"="+url.QueryEscape(q.Get(k)))
	}
	canon := strings.Join([]string{
		method,
		u.EscapedPath(),
		strings.Join(canonQ, "&"),
		"host:" + u.Host + "\n",
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	sum := sha256.Sum256([]byte(canon))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA256([]byte("AWS4"+s.SecretKey), date)
	k = hmacSHA256(k, s.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	q.Set("X-Amz-Signature", hex.EncodeToString(hmacSHA256(k, toSign)))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *S3) PresignPut(key, _ string, expiry time.Duration) (string, error) {
	return s.presign("PUT", key, expiry)
}
func (s *S3) PresignGet(key string, expiry time.Duration) (string, error) {
	return s.presign("GET", key, expiry)
}
