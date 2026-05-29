// Package wiki — filesystem-backed store for the llm-wiki layout.
//
// Layout assumed (matches dvystrcil/llm-wiki):
//
//	<root>/
//	  one-ring/
//	    entities/*.md
//	    concepts/*.md
//	    sources/*.md
//	    syntheses/*.md
//	  homelab/  (same shape)
//	  fiction/  (same shape)
//
// The store reads pages, searches by content + frontmatter, walks the
// link graph for neighbors, and writes new pages under the wiki tree.
// Write refuses to touch raw/ — that's immutable per AGENTS.md.
//
// Git sync is OUT of scope here. The deploy pod runs a git-sync sidecar
// that commits + pushes on a configurable interval. The MCP server only
// reads from + writes to the local mount.
package wiki

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PageTypeDirs are the four canonical type directories under each domain.
var PageTypeDirs = []string{"entities", "concepts", "sources", "syntheses"}

// typeForDir maps directory name → frontmatter `type:` value.
var typeForDir = map[string]string{
	"entities":  "entity",
	"concepts":  "concept",
	"sources":   "source",
	"syntheses": "synthesis",
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var domainRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var wikilinkRe = regexp.MustCompile(`\[\[([^\]|#]+)(?:#[^\]|]+)?(?:\|[^\]]+)?\]\]`)

// Store is a filesystem-backed view of the wiki tree.
type Store struct {
	root string
}

// Page is one parsed wiki page.
type Page struct {
	Domain      string
	Type        string
	TypeDir     string
	Slug        string
	Path        string
	Frontmatter map[string]string
	Aliases     []string
	Tags        []string
	Body        string
}

// NewStore returns a Store rooted at `wikiRoot`, which should be the directory
// containing the per-domain subdirectories (e.g. .../llm-wiki/wiki).
func NewStore(wikiRoot string) (*Store, error) {
	st, err := os.Stat(wikiRoot)
	if err != nil {
		return nil, fmt.Errorf("wiki root %q: %w", wikiRoot, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("wiki root %q is not a directory", wikiRoot)
	}
	abs, err := filepath.Abs(wikiRoot)
	if err != nil {
		return nil, err
	}
	return &Store{root: abs}, nil
}

// Root returns the absolute path the store is rooted at.
func (s *Store) Root() string { return s.root }

// ListDomains returns every directory directly under root that looks like a
// domain (lowercase alpha-numeric + dash, NOT a known type-dir or reserved name).
func (s *Store) ListDomains() ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	reserved := map[string]bool{
		"templates":    true,
		"lint-reports": true,
	}
	out := []string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if reserved[name] || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		if _, ok := typeForDir[name]; ok {
			continue
		}
		if !domainRe.MatchString(name) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// ListPages returns every page under wiki/<domain>/<type>/*.md.
func (s *Store) ListPages(domain string) ([]Page, error) {
	if !domainRe.MatchString(domain) {
		return nil, fmt.Errorf("invalid domain %q", domain)
	}
	out := []Page{}
	for _, tdir := range PageTypeDirs {
		typeDirPath := filepath.Join(s.root, domain, tdir)
		entries, err := os.ReadDir(typeDirPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			slug := strings.TrimSuffix(e.Name(), ".md")
			page, err := s.read(domain, tdir, slug)
			if err != nil {
				return nil, err
			}
			out = append(out, page)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

// Lookup returns one page by slug, searching all type-dirs in the domain.
func (s *Store) Lookup(domain, slug string) (Page, error) {
	if !domainRe.MatchString(domain) {
		return Page{}, fmt.Errorf("invalid domain %q", domain)
	}
	if !slugRe.MatchString(slug) {
		return Page{}, fmt.Errorf("invalid slug %q", slug)
	}
	for _, tdir := range PageTypeDirs {
		path := filepath.Join(s.root, domain, tdir, slug+".md")
		if _, err := os.Stat(path); err == nil {
			return s.read(domain, tdir, slug)
		}
	}
	return Page{}, fmt.Errorf("page %q not found in domain %q", slug, domain)
}

// Search returns pages where `query` (case-insensitive) appears in body,
// title (first h1), aliases, or tags. Results sorted by relevance.
func (s *Store) Search(domain, query string) ([]Page, error) {
	pages, err := s.ListPages(domain)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	type scored struct {
		page  Page
		score int
	}
	hits := []scored{}
	for _, p := range pages {
		score := 0
		for _, a := range p.Aliases {
			if strings.Contains(strings.ToLower(a), q) {
				score += 10
			}
		}
		for _, t := range p.Tags {
			if strings.Contains(strings.ToLower(t), q) {
				score += 5
			}
		}
		if strings.Contains(p.Slug, q) {
			score += 8
		}
		if strings.Contains(strings.ToLower(p.Body), q) {
			score++
		}
		if score > 0 {
			hits = append(hits, scored{page: p, score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].page.Slug < hits[j].page.Slug
	})
	out := make([]Page, len(hits))
	for i, h := range hits {
		out[i] = h.page
	}
	return out, nil
}

// Neighbors returns pages directly linked from the given page's body via
// `[[wikilink]]` syntax. Handles bare slugs, type-prefixed (`entities/del`),
// cross-section (`../syntheses/x`), and cross-domain (`../homelab/x`) forms.
// Only RESOLVED links are returned (broken links silently skipped).
func (s *Store) Neighbors(domain, slug string) ([]Page, error) {
	p, err := s.Lookup(domain, slug)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []Page{}
	for _, m := range wikilinkRe.FindAllStringSubmatch(p.Body, -1) {
		target := strings.TrimSpace(m[1])
		parts := strings.Split(target, "/")
		clean := []string{}
		for _, x := range parts {
			if x == "" || x == "." || x == ".." {
				continue
			}
			clean = append(clean, x)
		}
		if len(clean) == 0 {
			continue
		}
		linkSlug := strings.TrimSuffix(clean[len(clean)-1], ".md")
		if !slugRe.MatchString(linkSlug) {
			continue
		}
		candidatePage, err := s.Lookup(domain, linkSlug)
		if err != nil {
			found := false
			domains, _ := s.ListDomains()
			for _, d := range domains {
				if d == domain {
					continue
				}
				if cp, err := s.Lookup(d, linkSlug); err == nil {
					candidatePage = cp
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		key := candidatePage.Domain + "/" + candidatePage.Slug
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, candidatePage)
	}
	return out, nil
}

// HubEntry is one row in AuditReport.Hubs — a page ranked by how many
// other pages link to it.
type HubEntry struct {
	Slug          string `json:"slug"`
	IncomingCount int    `json:"incoming_count"`
}

// DanglingEntry is one row in AuditReport.Dangling — a wikilink target
// that doesn't resolve to any existing page in any domain.
type DanglingEntry struct {
	Slug           string   `json:"slug"`
	ReferencedFrom []string `json:"referenced_from"`
}

// AuditReport summarizes link-graph health for one domain. Reports state;
// does not mutate. Callers (model, script, human) decide what to do with
// the findings.
type AuditReport struct {
	Domain     string          `json:"domain"`
	TotalPages int             `json:"total_pages"`
	Hubs       []HubEntry      `json:"hubs"`
	Orphans    []string        `json:"orphans"`
	Dangling   []DanglingEntry `json:"dangling"`
}

// Audit walks every page in `domain`, extracts its wikilinks, and
// reports:
//   - Hubs: pages with the most incoming links (top-10, sorted desc).
//   - Orphans: pages with zero incoming links from inside `domain`.
//   - Dangling: wikilink targets that don't resolve to any page in any
//     domain (cross-domain matches are NOT flagged).
//
// Cross-section / cross-domain wikilinks are resolved the same way
// Neighbors does, so the audit and the navigation agree.
func (s *Store) Audit(domain string) (AuditReport, error) {
	pages, err := s.ListPages(domain)
	if err != nil {
		return AuditReport{}, err
	}
	report := AuditReport{
		Domain:     domain,
		TotalPages: len(pages),
		Hubs:       []HubEntry{},
		Orphans:    []string{},
		Dangling:   []DanglingEntry{},
	}

	inDomain := make(map[string]bool, len(pages))
	for _, p := range pages {
		inDomain[p.Slug] = true
	}

	incoming := make(map[string]map[string]bool)
	dangling := make(map[string]map[string]bool)

	otherDomains, _ := s.ListDomains()

	for _, p := range pages {
		seen := map[string]bool{}
		for _, m := range wikilinkRe.FindAllStringSubmatch(p.Body, -1) {
			target := strings.TrimSpace(m[1])
			parts := strings.Split(target, "/")
			clean := []string{}
			for _, x := range parts {
				if x == "" || x == "." || x == ".." {
					continue
				}
				clean = append(clean, x)
			}
			if len(clean) == 0 {
				continue
			}
			linkSlug := strings.TrimSuffix(clean[len(clean)-1], ".md")
			if !slugRe.MatchString(linkSlug) {
				continue
			}
			if linkSlug == p.Slug || seen[linkSlug] {
				continue
			}
			seen[linkSlug] = true

			if inDomain[linkSlug] {
				if incoming[linkSlug] == nil {
					incoming[linkSlug] = make(map[string]bool)
				}
				incoming[linkSlug][p.Slug] = true
				continue
			}
			// Not in domain — check other domains (mirrors Neighbors).
			resolved := false
			for _, d := range otherDomains {
				if d == domain {
					continue
				}
				if _, err := s.Lookup(d, linkSlug); err == nil {
					resolved = true
					break
				}
			}
			if resolved {
				continue
			}
			if dangling[linkSlug] == nil {
				dangling[linkSlug] = make(map[string]bool)
			}
			dangling[linkSlug][p.Slug] = true
		}
	}

	// Hubs: sort by IncomingCount desc, then slug asc for stable output.
	type pair struct {
		slug  string
		count int
	}
	hubPairs := make([]pair, 0, len(pages))
	for _, p := range pages {
		hubPairs = append(hubPairs, pair{p.Slug, len(incoming[p.Slug])})
	}
	sort.Slice(hubPairs, func(i, j int) bool {
		if hubPairs[i].count != hubPairs[j].count {
			return hubPairs[i].count > hubPairs[j].count
		}
		return hubPairs[i].slug < hubPairs[j].slug
	})
	const hubLimit = 10
	for i, h := range hubPairs {
		if i >= hubLimit {
			break
		}
		report.Hubs = append(report.Hubs, HubEntry{Slug: h.slug, IncomingCount: h.count})
	}

	// Orphans: zero incoming.
	for _, p := range pages {
		if len(incoming[p.Slug]) == 0 {
			report.Orphans = append(report.Orphans, p.Slug)
		}
	}
	sort.Strings(report.Orphans)

	// Dangling: sort by slug for stable output.
	for slug, sources := range dangling {
		refs := make([]string, 0, len(sources))
		for s := range sources {
			refs = append(refs, s)
		}
		sort.Strings(refs)
		report.Dangling = append(report.Dangling, DanglingEntry{
			Slug:           slug,
			ReferencedFrom: refs,
		})
	}
	sort.Slice(report.Dangling, func(i, j int) bool {
		return report.Dangling[i].Slug < report.Dangling[j].Slug
	})

	return report, nil
}

// Write creates or overwrites a page. Refuses if domain looks like a path
// traversal, if slug isn't a clean lowercase-kebab, if typeDir isn't one of
// the four canonical type-dirs, or if domain is "raw" (the raw/ tree is
// immutable per AGENTS.md).
func (s *Store) Write(domain, typeDir, slug, body string) error {
	if !domainRe.MatchString(domain) {
		return fmt.Errorf("invalid domain %q", domain)
	}
	if domain == "raw" {
		return errors.New("refuse to write under raw/ — that tree is immutable per AGENTS.md")
	}
	if !slugRe.MatchString(slug) {
		return fmt.Errorf("invalid slug %q (must match %s)", slug, slugRe.String())
	}
	validType := false
	for _, t := range PageTypeDirs {
		if typeDir == t {
			validType = true
			break
		}
	}
	if !validType {
		return fmt.Errorf("invalid typeDir %q (must be one of %v)", typeDir, PageTypeDirs)
	}
	dir := filepath.Join(s.root, domain, typeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	return nil
}

func (s *Store) read(domain, typeDir, slug string) (Page, error) {
	path := filepath.Join(s.root, domain, typeDir, slug+".md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return Page{}, err
	}
	fm, body := parseFrontmatter(string(raw))
	page := Page{
		Domain:      domain,
		Type:        typeForDir[typeDir],
		TypeDir:     typeDir,
		Slug:        slug,
		Path:        path,
		Frontmatter: fm,
		Body:        body,
	}
	page.Aliases = parseList(fm["aliases"])
	page.Tags = parseList(fm["tags"])
	return page, nil
}

func parseFrontmatter(text string) (map[string]string, string) {
	fm := map[string]string{}
	if !strings.HasPrefix(text, "---\n") {
		return fm, text
	}
	rest := strings.TrimPrefix(text, "---\n")
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return fm, text
	}
	block := rest[:end]
	body := rest[end+len("\n---\n"):]
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		fm[key] = val
	}
	return fm, body
}

func parseList(v string) []string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
	if inner == "" {
		return nil
	}
	out := []string{}
	for _, p := range strings.Split(inner, ",") {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
