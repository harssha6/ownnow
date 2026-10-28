package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3 is a minimal S3-compatible client (Cloudflare R2 first-class).
// R2: endpoint https://<account>.r2.cloudflarestorage.com, region "auto".
type S3 struct {
	Endpoint, Bucket, Region, AccessKey, SecretKey string
	HTTP                                           *http.Client
}

func (s *S3) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// SigV4 sign — https://docs.aws.amazon.com/IAM/latest/UserGuide/create-signed-request.html
func (s *S3) sign(req *http.Request, payloadHash string) {
	now := time.Now().UTC()
	req.Header.Set("x-amz-date", now.Format("20060102T150405Z"))
	req.Header.Set("x-amz-content-sha256", payloadHash)
	signed := "host;x-amz-content-sha256;x-amz-date"
	canon := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		"host:" + req.URL.Host + "\n" + "x-amz-content-sha256:" + payloadHash + "\n" + "x-amz-date:" + now.Format("20060102T150405Z") + "\n",
		signed,
		payloadHash,
	}, "\n")
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", now.Format("20060102"), s.Region)
	sum := sha256.Sum256([]byte(canon))
	toSign := "AWS4-HMAC-SHA256\n" + now.Format("20060102T150405Z") + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA256([]byte("AWS4"+s.SecretKey), now.Format("20060102"))
	k = hmacSHA256(k, s.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", s.AccessKey, scope, signed, sig))
}

func (s *S3) url(key string) string {
	// path-style: works for R2, MinIO, and AWS virtual-host fallback users can front with endpoint host
	esc := (&url.URL{Path: "/" + s.Bucket + "/" + key}).EscapedPath()
	return strings.TrimRight(s.Endpoint, "/") + esc
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64, ct string) error {
	body, err := io.ReadAll(r) // v1: buffer. TODO: streaming + multipart for >100MB
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.url(key), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	s.sign(req, hex.EncodeToString(sum[:]))
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("s3 put %s: %s %s", key, resp.Status, b)
	}
	return nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, int64, string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.url(key), nil)
	empty := sha256.Sum256(nil)
	s.sign(req, hex.EncodeToString(empty[:]))
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, 0, "", io.EOF
	}
	if resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, 0, "", fmt.Errorf("s3 get %s: %s", key, resp.Status)
	}
	return resp.Body, resp.ContentLength, resp.Header.Get("Content-Type"), nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, s.url(key), nil)
	empty := sha256.Sum256(nil)
	s.sign(req, hex.EncodeToString(empty[:]))
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("s3 delete %s: %s", key, resp.Status)
	}
	return nil
}

type listResult struct {
	Contents []struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
	} `xml:"Contents"`
	IsTruncated bool `xml:"IsTruncated"`
}

func (s *S3) List(ctx context.Context, prefix string) ([]Object, error) {
	u := strings.TrimRight(s.Endpoint, "/") + "/" + s.Bucket + "?list-type=2&prefix=" + url.QueryEscape(prefix)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	empty := sha256.Sum256(nil)
	s.sign(req, hex.EncodeToString(empty[:]))
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var lr listResult
	if err := xml.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, err
	}
	out := make([]Object, 0, len(lr.Contents))
	for _, c := range lr.Contents {
		out = append(out, Object{Key: c.Key, Size: c.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
