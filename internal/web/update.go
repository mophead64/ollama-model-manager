package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/mophead64/ollama-model-manager/internal/version"
)

const (
	latestReleaseURL = "https://api.github.com/repos/mophead64/ollama-model-manager/releases/latest"
	updateOKTTL      = 6 * time.Hour
	updateErrTTL     = 5 * time.Minute
	// A manual refresh bypasses the cache, but not more often than this, so a
	// click-happy user can't burn through GitHub's unauthenticated rate limit.
	updateMinGap = 30 * time.Second
)

type updateInfo struct {
	Available bool
	Current   bool // the latest release is the one running
	Latest    string
	Name      string // the release's title, if it has one
	URL       string
	Published time.Time
	Notes     template.HTML // the release notes, rendered from Markdown
	Checked   bool          // false if the check failed (offline, rate limited, ...)
}

// updateChecker asks GitHub for the latest release, caching the answer so
// dashboard loads don't each hit the API (unauthenticated calls are rate limited).
type updateChecker struct {
	client *http.Client
	url    string

	mu      sync.Mutex
	info    updateInfo
	expiry  time.Time
	fetched time.Time
}

func newUpdateChecker() *updateChecker {
	return &updateChecker{client: &http.Client{Timeout: 5 * time.Second}, url: latestReleaseURL}
}

// check returns the cached answer while it's fresh; force (a manual refresh)
// re-asks GitHub unless the last fetch was only moments ago.
func (u *updateChecker) check(ctx context.Context, force bool) updateInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now()
	if now.Before(u.expiry) && (!force || now.Sub(u.fetched) < updateMinGap) {
		return u.info
	}

	u.fetched = now
	info, err := u.fetch(ctx)
	if err != nil {
		u.info, u.expiry = updateInfo{}, time.Now().Add(updateErrTTL)
		return u.info
	}
	u.info, u.expiry = info, time.Now().Add(updateOKTTL)
	return info
}

func (u *updateChecker) fetch(ctx context.Context) (updateInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.url, nil)
	if err != nil {
		return updateInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ollama-model-manager/"+version.Version)
	resp, err := u.client.Do(req)
	if err != nil {
		return updateInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return updateInfo{}, fmt.Errorf("github returned %s", resp.Status)
	}
	var rel struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return updateInfo{}, err
	}
	notes, err := renderMarkdown(rel.Body)
	if err != nil {
		return updateInfo{}, fmt.Errorf("render release notes: %w", err)
	}
	return updateInfo{
		Checked:   true,
		Latest:    rel.TagName,
		Name:      rel.Name,
		URL:       rel.HTMLURL,
		Published: rel.PublishedAt,
		Notes:     notes,
		Current:   rel.TagName == version.Version,
		Available: version.Newer(rel.TagName, version.Version),
	}, nil
}

// releaseMarkdown renders release notes as GitHub does (tables, task lists,
// bare URLs as links). It's goldmark's safe mode: raw HTML in the notes is
// dropped and javascript: style links aren't linked, so what GitHub returns
// can go on the page as is.
var releaseMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(externalLinks{}, 100))),
)

func renderMarkdown(src string) (template.HTML, error) {
	var buf bytes.Buffer
	if err := releaseMarkdown.Convert([]byte(src), &buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// externalLinks opens the notes' links in a new tab, keeping the app open.
type externalLinks struct{}

func (externalLinks) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if l, ok := n.(*ast.Link); ok && entering {
			l.SetAttributeString("target", "_blank")
			l.SetAttributeString("rel", "noopener noreferrer")
		}
		return ast.WalkContinue, nil
	})
}

// handleVersionCheck is fetched by htmx after each page renders (and again
// every 6 hours while a page stays open), so the network round trip to GitHub
// never delays the page itself. ?force=1 is the footer's manual refresh.
func (s *Server) handleVersionCheck(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "version_status.html", map[string]any{
		"Version": version.Version,
		"Update":  s.updates.check(r.Context(), r.URL.Query().Get("force") != ""),
	})
}

// handleRelease fills in the Settings page's release panel with the latest
// release's notes, which say how to update.
func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "release_panel", map[string]any{
		"Version": version.Version,
		"Update":  s.updates.check(r.Context(), r.URL.Query().Get("force") != ""),
	})
}
