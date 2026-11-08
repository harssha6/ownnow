package server

import (
	"bytes"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
)

// AnalyticsConfig is optional instance-owned HTML injection, separate from
// built-in server counting. An empty block is off. Snippet is trusted HTML.
type AnalyticsConfig struct {
	Provider  string `json:"provider"` // umami, plausible, custom
	ScriptURL string `json:"scriptUrl,omitempty"`
	WebsiteID string `json:"websiteId,omitempty"`
	Domain    string `json:"domain,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
}

func (a AnalyticsConfig) enabled() bool { return a.Provider != "" }
func (a AnalyticsConfig) Validate() error {
	switch a.Provider {
	case "":
		return nil
	case "custom":
		if strings.TrimSpace(a.Snippet) == "" {
			return fmt.Errorf("analytics.snippet is required for custom provider")
		}
		return nil
	case "umami", "plausible":
		u, err := url.Parse(a.ScriptURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
			return fmt.Errorf("analytics.scriptUrl must be an http(s) URL")
		}
		if a.Provider == "umami" && a.WebsiteID == "" {
			return fmt.Errorf("analytics.websiteId is required for umami")
		}
		if a.Provider == "plausible" && a.Domain == "" {
			return fmt.Errorf("analytics.domain is required for plausible")
		}
		return nil
	default:
		return fmt.Errorf("unknown analytics.provider %q", a.Provider)
	}
}
func (a AnalyticsConfig) tag() string {
	switch a.Provider {
	case "umami":
		domain := ""
		if a.Domain != "" {
			domain = ` data-domains="` + html.EscapeString(a.Domain) + `"`
		}
		return `<script defer data-ownnow-analytics src="` + html.EscapeString(a.ScriptURL) + `" data-website-id="` + html.EscapeString(a.WebsiteID) + `"` + domain + `></script>`
	case "plausible":
		return `<script defer data-ownnow-analytics src="` + html.EscapeString(a.ScriptURL) + `" data-domain="` + html.EscapeString(a.Domain) + `"></script>`
	case "custom":
		return a.Snippet
	default:
		return ""
	}
}

// A small lexical HTML tag reader keeps injection dependency-free. It skips
// comments, quoted '>' characters and raw script/style text. It doesn't build a
// DOM or rewrite the publisher's markup.
type htmlTag struct {
	name       string
	closing    bool
	start, end int
	attrs      map[string]string
}

func htmlSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' }
func scanHTML(source string) []htmlTag {
	lowerBytes := []byte(source)
	for i, b := range lowerBytes {
		if b >= 'A' && b <= 'Z' {
			lowerBytes[i] = b + ('a' - 'A')
		}
	}
	lower := string(lowerBytes)
	var tags []htmlTag
	raw := ""
	for i := 0; i < len(source); {
		if raw != "" {
			end := -1
			for at := i; at < len(source); {
				offset := strings.Index(lower[at:], "</"+raw)
				if offset < 0 {
					break
				}
				candidate := at + offset
				next := candidate + 2 + len(raw)
				if next < len(source) && (htmlSpace(source[next]) || source[next] == '>' || source[next] == '/') {
					end = candidate
					break
				}
				at = next
			}
			if end < 0 {
				break
			}
			i = end
			raw = ""
		}
		n := strings.IndexByte(source[i:], '<')
		if n < 0 {
			break
		}
		start := i + n
		i = start + 1
		if strings.HasPrefix(source[start:], "<!--") {
			end := strings.Index(source[i+3:], "-->")
			if end < 0 {
				break
			}
			i += 3 + end + 3
			continue
		}
		closing := false
		if i < len(source) && source[i] == '/' {
			closing = true
			i++
		}
		nameStart := i
		for i < len(source) && ((source[i] >= 'a' && source[i] <= 'z') || (source[i] >= 'A' && source[i] <= 'Z') || (source[i] >= '0' && source[i] <= '9') || source[i] == '-') {
			i++
		}
		if i == nameStart {
			continue
		}
		tag := htmlTag{name: lower[nameStart:i], closing: closing, start: start, attrs: map[string]string{}}
		complete := false
		for i < len(source) {
			for i < len(source) && (htmlSpace(source[i]) || source[i] == '/') {
				i++
			}
			if i == len(source) {
				break
			}
			if source[i] == '>' {
				i++
				complete = true
				break
			}
			attrStart := i
			for i < len(source) && !htmlSpace(source[i]) && source[i] != '=' && source[i] != '>' && source[i] != '/' {
				i++
			}
			attr := lower[attrStart:i]
			if attr == "" {
				i++
				continue
			}
			for i < len(source) && htmlSpace(source[i]) {
				i++
			}
			value := ""
			if i < len(source) && source[i] == '=' {
				i++
				for i < len(source) && htmlSpace(source[i]) {
					i++
				}
				if i < len(source) && (source[i] == '\'' || source[i] == '"') {
					quote := source[i]
					i++
					begin := i
					for i < len(source) && source[i] != quote {
						i++
					}
					value = source[begin:i]
					if i < len(source) {
						i++
					}
				} else {
					begin := i
					for i < len(source) && !htmlSpace(source[i]) && source[i] != '>' {
						i++
					}
					value = source[begin:i]
				}
			}
			if _, exists := tag.attrs[attr]; !exists {
				tag.attrs[attr] = html.UnescapeString(value)
			}
		}
		if !complete {
			break
		}
		tag.end = i
		tags = append(tags, tag)
		if !closing && (tag.name == "script" || tag.name == "style" || tag.name == "textarea" || tag.name == "title") {
			raw = tag.name
		}
	}
	return tags
}
func (a AnalyticsConfig) inject(data []byte) []byte {
	if !a.enabled() {
		return data
	}
	source := string(data)
	tags := scanHTML(source)
	wanted := a.tag()
	customSources := map[string]bool{}
	if a.Provider == "custom" {
		// Exact configured snippets also cover inline trackers.
		if strings.Contains(source, wanted) {
			return data
		}
		for _, t := range scanHTML(wanted) {
			if t.name == "script" && t.attrs["src"] != "" {
				customSources[t.attrs["src"]] = true
			}
		}
	}
	insert := len(source)
	for _, t := range tags {
		if t.closing && (t.name == "head" || t.name == "body") && insert == len(source) {
			insert = t.start
		}
		if t.name != "script" || t.closing {
			continue
		}
		if _, ok := t.attrs["data-ownnow-analytics"]; ok {
			return data
		}
		if _, ok := t.attrs["data-website-id"]; ok {
			return data
		}
		if _, ok := t.attrs["data-domain"]; ok {
			return data
		}
		src := t.attrs["src"]
		if src != "" && (src == a.ScriptURL || customSources[src]) {
			return data
		}
		for _, tracker := range []string{"umami", "plausible", "googletagmanager.com", "google-analytics.com", "matomo", "posthog", "static.cloudflareinsights.com/beacon"} {
			if strings.Contains(strings.ToLower(src), tracker) {
				return data
			}
		}
	}
	return bytes.Join([][]byte{data[:insert], []byte(wanted), data[insert:]}, nil)
}

func (s *Server) injectHTML(w http.ResponseWriter, r *http.Request, site *Site, data []byte) []byte {
	if s.cfg.Analytics.enabled() {
		w.Header().Add("Vary", "DNT")
		w.Header().Add("Vary", "Sec-GPC")
	}
	if site.NoAnalytics || privacyRequested(r) {
		return data
	}
	return s.cfg.Analytics.inject(data)
}
