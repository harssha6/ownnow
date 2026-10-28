// Package store is a tiny object-store abstraction.
// Local disk for dev, S3-compatible (Cloudflare R2 / AWS S3 / MinIO) for prod.
// Stdlib only — no SDK, SigV4 is hand-rolled in s3.go. That is deliberate:
// one static binary, CGO_ENABLED=0, nothing to vendor.
package store

import (
	"context"
	"io"
)

type Object struct {
	Key  string
	Size int64
}

type Store interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, int64, string, error) // body, size, contentType
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]Object, error)
}

// CleanKey rejects traversal and normalises a user-supplied path.
func CleanKey(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	// reject absolute / traversal / backslash tricks up front
	for _, part := range split(p) {
		if part == ".." || part == "" {
			return "", false
		}
	}
	return join(split(p)), true
}

func split(p string) []string {
	var out []string
	cur := ""
	for _, c := range p {
		if c == '/' || c == '\\' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func join(parts []string) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += "/"
		}
		s += p
	}
	return s
}
