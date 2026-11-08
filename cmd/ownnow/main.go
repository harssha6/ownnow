// ownnow — your own here.now, in one Go binary.
//
//	ownnow serve                      # API + site serving (local disk or R2)
//	ownnow publish [--slug x] <path>  # file or folder -> live URL
//	ownnow list | delete <slug> | rollback <slug> <versionId>
//
// Env: OWNNOW_API_KEY, OWNNOW_SERVER (client), OWNNOW_DATA_DIR,
//
//	OWNNOW_ADDR (:8080), OWNNOW_BASE_DOMAIN, OWNNOW_PUBLIC_URL,
//	OWNNOW_STORAGE (local|s3), OWNNOW_S3_ENDPOINT, OWNNOW_S3_BUCKET,
//	OWNNOW_S3_REGION (auto for R2), OWNNOW_S3_ACCESS_KEY, OWNNOW_S3_SECRET_KEY,
//	OWNNOW_SECRET
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ownnow/internal/server"
	"ownnow/internal/store"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Version is set at release time: go build -ldflags "-X main.Version=v0.1.0"
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println("ownnow", Version)
	case "serve":
		err = serve()
	case "publish":
		err = publish(os.Args[2:])
	case "stats":
		err = stats(os.Args[2:])
	case "list":
		err = list()
	case "delete":
		if len(os.Args) < 3 {
			err = fmt.Errorf("usage: ownnow delete <slug>")
			break
		}
		err = del(os.Args[2])
	case "rollback":
		if len(os.Args) < 4 {
			err = fmt.Errorf("usage: ownnow rollback <slug> <versionId>")
			break
		}
		err = rollback(os.Args[2], os.Args[3])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("ownnow serve | publish [--slug x] [--ttl-hours n] [--password p] [--spa] [--index|--noindex] [--no-analytics] <file-or-dir> | stats <slug> [--days n] | list | delete <slug> | rollback <slug> <versionId> | version")
}

func serve() error {
	var config struct {
		Analytics server.AnalyticsConfig `json:"analytics"`
	}
	if file := os.Getenv("OWNNOW_CONFIG"); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return err
		}
	}
	if err := config.Analytics.Validate(); err != nil {
		return err
	}
	counting, err := strconv.ParseBool(env("OWNNOW_COUNTING", "true"))
	if err != nil {
		return fmt.Errorf("OWNNOW_COUNTING must be true or false")
	}
	var proxies []netip.Prefix
	for _, raw := range strings.Split(os.Getenv("OWNNOW_TRUSTED_PROXIES"), ",") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid OWNNOW_TRUSTED_PROXIES CIDR: %w", err)
		}
		proxies = append(proxies, prefix)
	}
	var st store.Store
	if env("OWNNOW_STORAGE", "local") == "s3" {
		st = &store.S3{
			Endpoint:  os.Getenv("OWNNOW_S3_ENDPOINT"),
			Bucket:    os.Getenv("OWNNOW_S3_BUCKET"),
			Region:    env("OWNNOW_S3_REGION", "auto"),
			AccessKey: os.Getenv("OWNNOW_S3_ACCESS_KEY"),
			SecretKey: os.Getenv("OWNNOW_S3_SECRET_KEY"),
		}
		fmt.Println("storage: S3/R2 bucket", os.Getenv("OWNNOW_S3_BUCKET"))
	} else {
		dir := env("OWNNOW_DATA_DIR", "./data")
		st, err = store.NewLocal(dir)
		if err != nil {
			return err
		}
		fmt.Println("storage: local", dir)
	}
	secret := []byte(os.Getenv("OWNNOW_SECRET"))
	if len(secret) == 0 {
		secret, err = storedSecret(st)
		if err != nil {
			return fmt.Errorf("load instance secret: %w", err)
		}
	}
	addr := env("OWNNOW_ADDR", ":8080")
	srv := server.New(server.Config{
		Store:            st,
		APIKey:           os.Getenv("OWNNOW_API_KEY"),
		BaseDomain:       os.Getenv("OWNNOW_BASE_DOMAIN"),
		PublicURL:        env("OWNNOW_PUBLIC_URL", "http://127.0.0.1"+addr),
		Secret:           secret,
		PresignedUploads: env("OWNNOW_STORAGE", "local") == "s3",
		DisableCounting:  !counting,
		Analytics:        config.Analytics,
		TrustedProxies:   proxies,
		EdgeStatsKey:     os.Getenv("OWNNOW_EDGE_STATS_KEY"),
	})
	if os.Getenv("OWNNOW_API_KEY") == "" {
		fmt.Println("warning: OWNNOW_API_KEY unset — local-dev mode, anyone can publish/mutate")
	}
	fmt.Println("ownnow listening on", addr)
	return http.ListenAndServe(addr, srv)
}

// ---------- client ----------

func apiBase() string { return strings.TrimRight(env("OWNNOW_SERVER", "http://127.0.0.1:8080"), "/") }

func doJSON(method, url string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if k := os.Getenv("OWNNOW_API_KEY"); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	if c := os.Getenv("OWNNOW_CLAIM"); c != "" {
		req.Header.Set("X-OwnNow-Claim", c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

type fileMeta struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
	abs         string
}

func collect(target string) ([]fileMeta, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []fileMeta{{Path: filepath.Base(target), Size: info.Size(), ContentType: mime.TypeByExtension(filepath.Ext(target)), abs: target}}, nil
	}
	var out []fileMeta
	err = filepath.Walk(target, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(target, p)
		out = append(out, fileMeta{Path: filepath.ToSlash(rel), Size: info.Size(), ContentType: mime.TypeByExtension(filepath.Ext(p)), abs: p})
		return nil
	})
	return out, err
}

func publish(args []string) error {
	var slug, password string
	var ttl int
	spa := false
	var indexable *bool // nil = leave the server's value alone
	var target string
	var noAnalytics *bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--slug":
			i++
			slug = args[i]
		case "--password":
			i++
			password = args[i]
		case "--ttl-hours":
			i++
			fmt.Sscan(args[i], &ttl)
		case "--no-analytics":
			v := true
			noAnalytics = &v
		case "--analytics":
			v := false
			noAnalytics = &v
		case "--spa":
			spa = true
		case "--index", "--noindex":
			v := args[i] == "--index"
			indexable = &v
		default:
			target = args[i]
		}
	}
	if target == "" {
		return fmt.Errorf("usage: ownnow publish [--slug x] <file-or-dir>")
	}
	files, err := collect(target)
	if err != nil {
		return err
	}
	metas := make([]map[string]any, len(files))
	for i, f := range files {
		metas[i] = map[string]any{"path": f.Path, "size": f.Size, "contentType": f.ContentType}
	}
	method, url := "POST", apiBase()+"/api/v1/publish"
	if slug != "" {
		method, url = "PUT", apiBase()+"/api/v1/publish/"+slug
	}
	var created struct {
		Slug       string `json:"slug"`
		VersionID  string `json:"versionId"`
		SiteURL    string `json:"siteUrl"`
		ClaimToken string `json:"claimToken"`
		Uploads    []struct {
			Path string `json:"path"`
			URL  string `json:"url"`
		} `json:"uploads"`
	}
	body := map[string]any{"files": metas, "password": password, "ttlHours": ttl, "spa": spa}
	if noAnalytics != nil {
		body["noAnalytics"] = *noAnalytics
	}
	if indexable != nil {
		body["indexable"] = *indexable
	}
	if err := doJSON(method, url, body, &created); err != nil {
		return err
	}
	byPath := map[string]string{}
	for _, f := range files {
		byPath[f.Path] = f.abs
	}
	for _, up := range created.Uploads {
		data, err := os.ReadFile(byPath[up.Path])
		if err != nil {
			return err
		}
		uploadURL := up.URL
		if !strings.HasPrefix(uploadURL, "http") { // relative = through our server; absolute = presigned R2
			uploadURL = apiBase() + uploadURL
		}
		req, _ := http.NewRequestWithContext(context.Background(), "PUT", uploadURL, bytes.NewReader(data))
		req.Header.Set("Content-Type", mime.TypeByExtension(filepath.Ext(up.Path)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("upload %s: %s", up.Path, resp.Status)
		}
		fmt.Println("  uploaded", up.Path)
	}
	var fin struct {
		SiteURL string `json:"siteUrl"`
	}
	if err := doJSON("POST", apiBase()+"/api/v1/publish/"+created.Slug+"/finalize", map[string]any{"versionId": created.VersionID}, &fin); err != nil {
		return err
	}
	fmt.Println(fin.SiteURL)
	if created.ClaimToken != "" {
		fmt.Println("claim token (save it):", created.ClaimToken)
	}
	return nil
}

func list() error {
	var out struct {
		Sites []struct {
			Slug    string `json:"slug"`
			Current string `json:"currentVersion"`
		} `json:"sites"`
	}
	if err := doJSON("GET", apiBase()+"/api/v1/publishes", nil, &out); err != nil {
		return err
	}
	for _, s := range out.Sites {
		fmt.Printf("%s  %s\n", s.Slug, s.Current)
	}
	return nil
}
func del(slug string) error {
	return doJSON("DELETE", apiBase()+"/api/v1/publish/"+slug, nil, nil)
}
func rollback(slug, version string) error {
	var out map[string]any
	if err := doJSON("POST", apiBase()+"/api/v1/publish/"+slug+"/rollback", map[string]any{"versionId": version}, &out); err != nil {
		return err
	}
	fmt.Println(out["siteUrl"], "->", out["currentVersion"])
	return nil
}

func stats(args []string) error {
	days := 7
	slug := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--days" {
			i++
			if i == len(args) {
				return fmt.Errorf("--days requires a number")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > 365 {
				return fmt.Errorf("--days must be between 1 and 365")
			}
			days = n
		} else if slug == "" && !strings.HasPrefix(args[i], "-") {
			slug = args[i]
		} else {
			return fmt.Errorf("usage: ownnow stats <slug> [--days N]")
		}
	}
	if slug == "" || strings.ContainsAny(slug, "/?#") {
		return fmt.Errorf("usage: ownnow stats <slug> [--days N]")
	}
	var out server.StatsResponse
	if err := doJSON("GET", fmt.Sprintf("%s/api/v1/publishes/%s/stats?days=%d", apiBase(), slug, days), nil, &out); err != nil {
		return err
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// Retain the generated key across restarts so daily uniques and existing signed
// cookies don't reset when OWNNOW_SECRET is omitted in single-binary mode.
func storedSecret(st store.Store) ([]byte, error) {
	const key = "_meta/instance-secret"
	rc, _, _, err := st.Get(context.Background(), key)
	if err == nil {
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, 33))
		if err != nil {
			return nil, err
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("invalid stored secret")
		}
		return b, nil
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := st.Put(context.Background(), key, bytes.NewReader(b), int64(len(b)), "application/octet-stream"); err != nil {
		return nil, err
	}
	return b, nil
}
