// Package server implements the ownnow publish API + static serving.
//
// Publish flow mirrors here.now's three steps (so agents/skills feel familiar):
//  1. POST /api/v1/publish            -> {slug, versionId, uploads:[{path,url}]}
//  2. PUT  each uploads[].url         -> raw file bytes
//  3. POST /api/v1/publish/:slug/finalize {versionId} -> site goes live atomically
//
// A site is never live until finalize. Old versions stay put = free rollback.
package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"ownnow/internal/store"
)

type FileEntry struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
}

type Version struct {
	ID        string      `json:"id"`
	CreatedAt time.Time   `json:"createdAt"`
	Files     []FileEntry `json:"files"`
}

type Site struct {
	Slug         string     `json:"slug"`
	DisplayName  string     `json:"displayName,omitempty"`
	Description  string     `json:"displayDescription,omitempty"`
	Current      string     `json:"currentVersion,omitempty"`
	Versions     []Version  `json:"versions,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	PasswordHash string     `json:"-"`
	ClaimHash    string     `json:"-"`
	SPA          bool       `json:"spa,omitempty"`
	COI          bool       `json:"crossOriginIsolation,omitempty"`
	Anonymous    bool       `json:"anonymous,omitempty"`
}

type Config struct {
	Store      store.Store
	APIKey     string // empty = anonymous-only mode (still requires claim token to mutate a site)
	BaseDomain string // e.g. "now.example.com" -> {slug}.now.example.com
	PublicURL  string // e.g. http://127.0.0.1:8080 (path mode display)
	Secret     []byte // cookie / upload-token HMAC key
	// PresignedUploads: when the store can presign (R2/S3), upload URLs point
	// straight at object storage. Required behind Vercel's 4.5 MB body cap.
	PresignedUploads bool
}

type Server struct {
	cfg Config
	mux *http.ServeMux
}

func New(cfg Config) *Server {
	if len(cfg.Secret) == 0 {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		cfg.Secret = b
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	m.HandleFunc("POST /api/v1/publish", s.create)
	m.HandleFunc("PUT /api/v1/publish/{slug}", s.create) // update = create a new pending version
	m.HandleFunc("PUT /api/v1/upload/{slug}/{version}/{path...}", s.upload)
	m.HandleFunc("POST /api/v1/publish/{slug}/finalize", s.finalize)
	m.HandleFunc("PATCH /api/v1/publish/{slug}/metadata", s.metadata)
	m.HandleFunc("GET /api/v1/publish/{slug}", s.getSite)
	m.HandleFunc("DELETE /api/v1/publish/{slug}", s.deleteSite)
	m.HandleFunc("POST /api/v1/publish/{slug}/rollback", s.rollback)
	m.HandleFunc("GET /api/v1/publish/{slug}/versions", s.versions)
	m.HandleFunc("GET /api/v1/publish/{slug}/files", s.files)
	m.HandleFunc("POST /api/v1/publish/{slug}/unlock", s.unlock)
	m.HandleFunc("GET /api/v1/publishes", s.list)
	m.HandleFunc("/", s.serveSite) // catch-all: subdomain or /{slug}/...
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg, "code": code})
}

func (s *Server) mac(parts ...string) string {
	h := hmac.New(sha256.New, s.cfg.Secret)
	h.Write([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h.Sum(nil))
}
func hashStr(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }

func (s *Server) authed(r *http.Request) bool {
	if s.cfg.APIKey == "" {
		return false
	}
	return hmac.Equal([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), []byte(s.cfg.APIKey))
}

// canMutate: API key, or the anonymous claim token for that site, or (no API key configured at all = local dev)
func (s *Server) canMutate(r *http.Request, site *Site) bool {
	if s.cfg.APIKey == "" {
		return true
	}
	if s.authed(r) {
		return true
	}
	if site != nil && site.ClaimHash != "" {
		tok := r.Header.Get("X-OwnNow-Claim")
		if tok == "" {
			tok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		return hmac.Equal([]byte(hashStr(tok)), []byte(site.ClaimHash))
	}
	return false
}

func (s *Server) metaKey(slug string) string { return "_meta/sites/" + slug + ".json" }
func (s *Server) fileKey(slug, version, p string) string {
	return "sites/" + slug + "/" + version + "/" + p
}

func (s *Server) loadSite(r *http.Request, slug string) (*Site, error) {
	rc, _, _, err := s.cfg.Store.Get(r.Context(), s.metaKey(slug))
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var site Site
	if err := json.NewDecoder(rc).Decode(&site); err != nil {
		return nil, err
	}
	// password/claim hashes live in a sidecar so Site JSON stays shareable
	if rc2, _, _, err := s.cfg.Store.Get(r.Context(), "_meta/secrets/"+slug+".json"); err == nil {
		defer rc2.Close()
		var sec struct{ PasswordHash, ClaimHash string }
		_ = json.NewDecoder(rc2).Decode(&sec)
		site.PasswordHash, site.ClaimHash = sec.PasswordHash, sec.ClaimHash
	}
	return &site, nil
}

func (s *Server) saveSite(r *http.Request, site *Site) error {
	b, _ := json.Marshal(site)
	if err := s.cfg.Store.Put(r.Context(), s.metaKey(site.Slug), strings.NewReader(string(b)), int64(len(b)), "application/json"); err != nil {
		return err
	}
	sec, _ := json.Marshal(map[string]string{"PasswordHash": site.PasswordHash, "ClaimHash": site.ClaimHash})
	return s.cfg.Store.Put(r.Context(), "_meta/secrets/"+site.Slug+".json", strings.NewReader(string(sec)), int64(len(sec)), "application/json")
}

func (s *Server) siteURL(slug string) string {
	if s.cfg.BaseDomain != "" {
		return "https://" + slug + "." + s.cfg.BaseDomain + "/"
	}
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	return base + "/" + slug + "/"
}

var adjs = []string{"bright", "calm", "swift", "amber", "cosmic", "quiet", "bold", "lucky", "solar", "velvet"}
var nouns = []string{"canvas", "harbor", "meadow", "orbit", "pixel", "river", "signal", "summit", "voyage", "willow"}

func randSlug() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s", adjs[int(b[0])%len(adjs)], nouns[int(b[1])%len(nouns)], hex.EncodeToString(b[2:])[:2]+fmt.Sprintf("%02d", int(b[2])%100))
}
func randID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}
func validSlug(s string) bool {
	if len(s) < 1 || len(s) > 63 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return !strings.HasPrefix(s, "-") && !strings.HasSuffix(s, "-")
}

// ---------- API ----------

type createReq struct {
	Slug        string      `json:"slug,omitempty"`
	Files       []FileEntry `json:"files"`
	TTLHours    int         `json:"ttlHours,omitempty"`
	Password    string      `json:"password,omitempty"`
	SPA         bool        `json:"spa,omitempty"`
	COI         bool        `json:"crossOriginIsolation,omitempty"`
	DisplayName string      `json:"displayName,omitempty"`
	Description string      `json:"displayDescription,omitempty"`
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil || len(req.Files) == 0 {
		fail(w, 400, "files[] required")
		return
	}
	if len(req.Files) > 2500 {
		fail(w, 400, "max 2500 files per site (same as here.now)")
		return
	}
	slug := req.Slug
	if slug == "" && r.PathValue("slug") != "" {
		slug = r.PathValue("slug")
	}
	authed := s.authed(r)
	var site *Site
	var claim string
	if slug != "" {
		if !validSlug(slug) {
			fail(w, 400, "invalid slug")
			return
		}
		existing, err := s.loadSite(r, slug)
		if err == nil {
			if !s.canMutate(r, existing) {
				fail(w, 403, "slug taken or claim token required")
				return
			}
			site = existing
		}
	}
	if site == nil {
		if slug == "" {
			slug = randSlug()
		}
		site = &Site{Slug: slug, CreatedAt: time.Now().UTC(), Anonymous: !authed && s.cfg.APIKey != ""}
		if !authed {
			claim = randID("claim_")
			site.ClaimHash = hashStr(claim)
			exp := time.Now().UTC().Add(24 * time.Hour) // anonymous = 24h, like here.now
			site.ExpiresAt = &exp
		}
	}
	if req.TTLHours > 0 {
		exp := time.Now().UTC().Add(time.Duration(req.TTLHours) * time.Hour)
		site.ExpiresAt = &exp
	}
	if req.Password != "" {
		site.PasswordHash = hashStr(req.Password)
	}
	site.SPA, site.COI = req.SPA, req.COI
	if req.DisplayName != "" {
		site.DisplayName = req.DisplayName
	}
	if req.Description != "" {
		site.Description = req.Description
	}
	version := Version{ID: randID("v_"), CreatedAt: time.Now().UTC()}
	uploads := make([]map[string]any, 0, len(req.Files))
	for _, f := range req.Files {
		clean, ok := store.CleanKey(f.Path)
		if !ok {
			fail(w, 400, "invalid file path: "+f.Path)
			return
		}
		version.Files = append(version.Files, FileEntry{Path: clean, Size: f.Size, ContentType: f.ContentType})
		uploadURL := ""
		if s.cfg.PresignedUploads {
			if p, ok := s.cfg.Store.(store.Presigner); ok {
				u, err := p.PresignPut(s.fileKey(slug, version.ID, clean), f.ContentType, time.Hour)
				if err == nil {
					uploadURL = u
				}
			}
		}
		if uploadURL == "" { // fallback: upload through this server (local dev)
			token := s.mac(slug, version.ID, clean)
			uploadURL = "/api/v1/upload/" + slug + "/" + version.ID + "/" + clean + "?token=" + token
		}
		uploads = append(uploads, map[string]any{
			"path": clean,
			"url":  uploadURL,
		})
	}
	// stash pending version inside site doc under a temp field via Versions + not-current marker:
	// simple approach: store pending manifest separately
	pb, _ := json.Marshal(version)
	_ = s.cfg.Store.Put(r.Context(), "_meta/pending/"+slug+"/"+version.ID+".json", strings.NewReader(string(pb)), int64(len(pb)), "application/json")
	if err := s.saveSite(r, site); err != nil {
		fail(w, 500, err.Error())
		return
	}
	resp := map[string]any{"slug": slug, "versionId": version.ID, "uploads": uploads, "siteUrl": s.siteURL(slug)}
	if claim != "" {
		resp["claimToken"] = claim
		resp["claimUrl"] = s.siteURL(slug) + "?claim=" + claim
		resp["note"] = "anonymous site expires in 24h — save claimToken to update/delete it"
	}
	writeJSON(w, 201, resp)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	slug, version, p := r.PathValue("slug"), r.PathValue("version"), r.PathValue("path")
	clean, ok := store.CleanKey(p)
	if !ok || r.URL.Query().Get("token") != s.mac(slug, version, clean) {
		fail(w, 403, "bad upload token or path")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 512<<20))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ct := r.Header.Get("Content-Type")
	if err := s.cfg.Store.Put(r.Context(), s.fileKey(slug, version, clean), strings.NewReader(string(body)), int64(len(body)), ct); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": clean, "size": len(body), "sha256": hashStr(string(body))})
}

func (s *Server) finalize(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	site, err := s.loadSite(r, slug)
	if err != nil {
		fail(w, 404, "site not found")
		return
	}
	if !s.canMutate(r, site) {
		fail(w, 403, "claim token or API key required")
		return
	}
	var body struct {
		VersionID string `json:"versionId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	rc, _, _, err := s.cfg.Store.Get(r.Context(), "_meta/pending/"+slug+"/"+body.VersionID+".json")
	if err != nil {
		fail(w, 404, "pending version not found (already finalized?)")
		return
	}
	defer rc.Close()
	var version Version
	if err := json.NewDecoder(rc).Decode(&version); err != nil {
		fail(w, 500, err.Error())
		return
	}
	// verify every declared file actually arrived before going live
	for _, f := range version.Files {
		objs, err := s.cfg.Store.List(r.Context(), s.fileKey(slug, version.ID, f.Path))
		if err != nil || len(objs) == 0 {
			fail(w, 400, "missing uploaded file: "+f.Path)
			return
		}
	}
	site.Versions = append(site.Versions, version)
	site.Current = version.ID // <-- the atomic switch. Old version untouched.
	_ = s.cfg.Store.Delete(r.Context(), "_meta/pending/"+slug+"/"+version.ID+".json")
	if err := s.saveSite(r, site); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"slug": slug, "versionId": version.ID, "siteUrl": s.siteURL(slug), "live": true})
}

func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	site, err := s.loadSite(r, slug)
	if err != nil {
		fail(w, 404, "site not found")
		return
	}
	if !s.canMutate(r, site) {
		fail(w, 403, "forbidden")
		return
	}
	var patch struct {
		DisplayName *string `json:"displayName"`
		Description *string `json:"displayDescription"`
		Password    *string `json:"password"`
		SPA         *bool   `json:"spa"`
		COI         *bool   `json:"crossOriginIsolation"`
		TTLHours    *int    `json:"ttlHours"`
	}
	_ = json.NewDecoder(r.Body).Decode(&patch)
	if patch.DisplayName != nil {
		site.DisplayName = *patch.DisplayName
	}
	if patch.Description != nil {
		site.Description = *patch.Description
	}
	if patch.Password != nil {
		if *patch.Password == "" {
			site.PasswordHash = ""
		} else {
			site.PasswordHash = hashStr(*patch.Password)
		}
	}
	if patch.SPA != nil {
		site.SPA = *patch.SPA
	}
	if patch.COI != nil {
		site.COI = *patch.COI
	}
	if patch.TTLHours != nil {
		if *patch.TTLHours <= 0 {
			site.ExpiresAt = nil
		} else {
			exp := time.Now().UTC().Add(time.Duration(*patch.TTLHours) * time.Hour)
			site.ExpiresAt = &exp
		}
	}
	_ = s.saveSite(r, site)
	writeJSON(w, 200, site)
}

func (s *Server) getSite(w http.ResponseWriter, r *http.Request) {
	site, err := s.loadSite(r, r.PathValue("slug"))
	if err != nil {
		fail(w, 404, "site not found")
		return
	}
	writeJSON(w, 200, map[string]any{"site": site, "siteUrl": s.siteURL(site.Slug)})
}
func (s *Server) versions(w http.ResponseWriter, r *http.Request) {
	site, err := s.loadSite(r, r.PathValue("slug"))
	if err != nil {
		fail(w, 404, "site not found")
		return
	}
	writeJSON(w, 200, map[string]any{"slug": site.Slug, "currentVersion": site.Current, "versions": site.Versions})
}
func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	site, err := s.loadSite(r, r.PathValue("slug"))
	if err != nil {
		fail(w, 404, "site not found")
		return
	}
	for _, v := range site.Versions {
		if v.ID == site.Current {
			writeJSON(w, 200, map[string]any{"versionId": v.ID, "files": v.Files})
			return
		}
	}
	writeJSON(w, 200, map[string]any{"files": []any{}})
}
func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	site, err := s.loadSite(r, slug)
	if err != nil {
		fail(w, 404, "site not found")
		return
	}
	if !s.canMutate(r, site) {
		fail(w, 403, "forbidden")
		return
	}
	var body struct {
		VersionID string `json:"versionId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, v := range site.Versions {
		if v.ID == body.VersionID {
			site.Current = v.ID
			_ = s.saveSite(r, site)
			writeJSON(w, 200, map[string]any{"slug": slug, "currentVersion": v.ID, "siteUrl": s.siteURL(slug)})
			return
		}
	}
	fail(w, 404, "version not found")
}
func (s *Server) deleteSite(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	site, err := s.loadSite(r, slug)
	if err != nil {
		fail(w, 404, "site not found")
		return
	}
	if !s.canMutate(r, site) {
		fail(w, 403, "forbidden")
		return
	}
	objs, _ := s.cfg.Store.List(r.Context(), "sites/"+slug+"/")
	for _, o := range objs {
		_ = s.cfg.Store.Delete(r.Context(), o.Key)
	}
	_ = s.cfg.Store.Delete(r.Context(), s.metaKey(slug))
	_ = s.cfg.Store.Delete(r.Context(), "_meta/secrets/"+slug+".json")
	writeJSON(w, 200, map[string]any{"deleted": slug})
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	if s.cfg.APIKey != "" && !s.authed(r) {
		fail(w, 401, "API key required")
		return
	}
	objs, _ := s.cfg.Store.List(r.Context(), "_meta/sites/")
	var sites []*Site
	for _, o := range objs {
		slug := strings.TrimSuffix(path.Base(o.Key), ".json")
		if st, err := s.loadSite(r, slug); err == nil {
			sites = append(sites, st)
		}
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].CreatedAt.After(sites[j].CreatedAt) })
	writeJSON(w, 200, map[string]any{"sites": sites})
}

func (s *Server) unlock(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	site, err := s.loadSite(r, slug)
	if err != nil {
		fail(w, 404, "site not found")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if site.PasswordHash == "" || hashStr(body.Password) != site.PasswordHash {
		fail(w, 401, "wrong password")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "ownnow_unlock_" + slug, Value: s.mac("unlock", slug, site.PasswordHash), Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- serving ----------

func (s *Server) resolveSlug(r *http.Request) (slug, rest string) {
	// subdomain mode: {slug}.baseDomain
	if s.cfg.BaseDomain != "" {
		host := strings.Split(r.Host, ":")[0]
		if strings.HasSuffix(host, "."+s.cfg.BaseDomain) {
			return strings.TrimSuffix(host, "."+s.cfg.BaseDomain), strings.TrimPrefix(r.URL.Path, "/")
		}
	}
	// path mode: /{slug}/...
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	if i := strings.Index(trimmed, "/"); i >= 0 {
		return trimmed[:i], trimmed[i+1:]
	}
	return trimmed, ""
}

func (s *Server) serveSite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(w, 405, "method not allowed")
		return
	}
	slug, rest := s.resolveSlug(r)
	if slug == "" || slug == "api" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>ownnow</title><h1>ownnow</h1><p>Your own here.now. Publish: <code>ownnow publish ./site</code></p><p><a href="/api/v1/publishes">/api/v1/publishes</a> · <a href="/healthz">/healthz</a></p>`)
		return
	}
	site, err := s.loadSite(r, slug)
	if err != nil || site.Current == "" {
		http.NotFound(w, r)
		return
	}
	if site.ExpiresAt != nil && time.Now().UTC().After(*site.ExpiresAt) {
		w.WriteHeader(http.StatusGone)
		fmt.Fprint(w, "this site has expired")
		return
	}
	if site.COI {
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
	}
	if site.PasswordHash != "" {
		c, err := r.Cookie("ownnow_unlock_" + slug)
		if err != nil || c.Value != s.mac("unlock", slug, site.PasswordHash) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `<!doctype html><title>Locked</title><h1>🔒 %s</h1><form onsubmit="event.preventDefault();fetch('/api/v1/publish/%s/unlock',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({password:this.p.value})}).then(r=>r.ok?location.reload():alert('wrong password'))"><input name=p type=password placeholder=password autofocus><button>Unlock</button></form>`, html.EscapeString(slug), slug)
			return
		}
	}
	var cur *Version
	for i := range site.Versions {
		if site.Versions[i].ID == site.Current {
			cur = &site.Versions[i]
		}
	}
	if cur == nil {
		http.NotFound(w, r)
		return
	}
	if rest == "" {
		rest = "index.html"
	}
	// exact file?
	for _, f := range cur.Files {
		if f.Path == rest {
			s.serveFile(w, r, slug, cur.ID, f)
			return
		}
	}
	// directory index: rest + "/index.html"
	for _, f := range cur.Files {
		if f.Path == strings.TrimSuffix(rest, "/")+"/index.html" {
			s.serveFile(w, r, slug, cur.ID, f)
			return
		}
	}
	// single-html fallback (opennow does this too): one html file, no index
	if rest == "index.html" {
		var htmls []FileEntry
		for _, f := range cur.Files {
			if strings.HasSuffix(f.Path, ".html") && !strings.Contains(f.Path, "/") {
				htmls = append(htmls, f)
			}
		}
		if len(htmls) == 1 {
			s.serveFile(w, r, slug, cur.ID, htmls[0])
			return
		}
		// no index at all -> directory listing (here.now does this for file bundles)
		s.listing(w, site, cur)
		return
	}
	if site.SPA {
		for _, f := range cur.Files {
			if f.Path == "index.html" {
				s.serveFile(w, r, slug, cur.ID, f)
				return
			}
		}
	}
	// custom 404.html, like here.now
	for _, f := range cur.Files {
		if f.Path == "404.html" {
			rc, _, _, err := s.cfg.Store.Get(r.Context(), s.fileKey(slug, cur.ID, f.Path))
			if err == nil {
				defer rc.Close()
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.Copy(w, rc)
				return
			}
		}
	}
	http.NotFound(w, r)
}

func (s *Server) listing(w http.ResponseWriter, site *Site, v *Version) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><title>%s</title><h1>%s</h1><ul>`, html.EscapeString(site.Slug), html.EscapeString(site.Slug))
	for _, f := range v.Files {
		fmt.Fprintf(w, `<li><a href="%s">%s</a> (%d bytes)</li>`, html.EscapeString(f.Path), html.EscapeString(f.Path), f.Size)
	}
	fmt.Fprint(w, `</ul>`)
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, slug, version string, f FileEntry) {
	rc, _, _, err := s.cfg.Store.Get(r.Context(), s.fileKey(slug, version, f.Path))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ct := f.ContentType
	if ct == "" {
		ct = mime.TypeByExtension(path.Ext(f.Path))
	}
	if strings.HasSuffix(f.Path, ".wasm") {
		ct = "application/wasm"
	}
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", ct)
	if strings.HasSuffix(f.Path, ".wasm") {
		w.Header().Set("Cache-Control", "no-cache") // here.now parity: wasm revalidates
	} else {
		w.Header().Set("Cache-Control", "public, max-age=60")
	}
	w.Header().Set("ETag", `"`+hashStr(string(data))[:16]+`"`)
	http.ServeContent(w, r, f.Path, time.Time{}, strings.NewReader(string(data))) // gives Range support
}
