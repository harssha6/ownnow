package store

import (
	"context"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Local struct{ Root string }

func NewLocal(root string) (*Local, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Local{Root: root}, nil
}

func (l *Local) path(key string) (string, error) {
	clean, ok := CleanKey(key)
	if !ok {
		return "", os.ErrInvalid
	}
	p := filepath.Join(l.Root, filepath.FromSlash(clean))
	// belt-and-braces: never escape Root
	if !strings.HasPrefix(p, filepath.Clean(l.Root)+string(os.PathSeparator)) && p != filepath.Clean(l.Root) {
		return "", os.ErrInvalid
	}
	return p, nil
}

func (l *Local) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p) // atomic-ish publish of a single object
}

func (l *Local) Get(_ context.Context, key string) (io.ReadCloser, int64, string, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, 0, "", err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, "", err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, "", err
	}
	ct := mime.TypeByExtension(filepath.Ext(p))
	return f, st.Size(), ct, nil
}

func (l *Local) Delete(_ context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (l *Local) List(_ context.Context, prefix string) ([]Object, error) {
	var out []Object
	root := filepath.Clean(l.Root)
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasSuffix(p, ".tmp") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		k := filepath.ToSlash(rel)
		if strings.HasPrefix(k, prefix) {
			out = append(out, Object{Key: k, Size: info.Size()})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, err
}
