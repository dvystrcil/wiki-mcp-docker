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
	"sync"
	"time"
	"unicode"
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

// ErrPageNotFound reports a well-formed (domain, slug) that resolves to no
// page on disk. Callers must be able to tell this apart from a validation
// error: only a missing page has a recovery path worth offering.
var ErrPageNotFound = errors.New("page not found")

// Store is a filesystem-backed view of the wiki tree.
type Store struct {
	root string

	// llm-wiki scripts/schema.json for wiki_write's check (wiki-mcp-docker#1).
	// Re-read whenever the file appears or its mtime changes: it lives in the
	// git-synced checkout, which can land after startup (2026-10-09: git-sync
	// was stuck, the pod started without it, and a startup-only load left the
	// check off until a restart). nil schema = check off.
	schemaMu    sync.Mutex
	schemaPath  string
	schemaMtime time.Time
	schema      *Schema
}

// SetSchema enables wiki_write's schema check with a fixed schema (tests).
func (s *Store) SetSchema(sc *Schema) {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	s.schema, s.schemaPath = sc, ""
}

// SetSchemaPath enables the check from a file, re-read when it changes.
func (s *Store) SetSchemaPath(path string) {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	s.schemaPath, s.schemaMtime, s.schema = path, time.Time{}, nil
}

func (s *Store) currentSchema() *Schema {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaPath == "" {
		return s.schema
	}
	st, err := os.Stat(s.schemaPath)
	if err != nil {
		s.schema = nil
		return nil
	}
	if s.schema == nil || !st.ModTime().Equal(s.schemaMtime) {
		if sc, err := LoadSchema(s.schemaPath); err == nil {
			s.schema, s.schemaMtime = sc, st.ModTime()
		}
	}
	return s.schema
}

// ValidatePage checks a body against the schema. checked is false when no
// schema is available, so a caller can never mistake "not checked" for "clean".
func (s *Store) ValidatePage(domain, typeDir, body string) (violations []Violation, checked bool) {
	sc := s.currentSchema()
	if sc == nil {
		return []Violation{}, false
	}
	return sc.Validate(domain, typeDir, body), true
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
	return Page{}, fmt.Errorf("%w: %q in domain %q", ErrPageNotFound, slug, domain)
}

// Search returns the pages that match any word of `query`, best first.
//
// The query is lowercased and split into words on anything that is not a
// letter or digit ('name-lookup' -> name, lookup). Stop words and one-letter
// words are dropped, unless that would leave nothing to search for. Each word
// is matched as a substring of each field, and a page earns, per word, the
// weight of every field it hits: alias 10, slug 8, title (first h1) 8, tag 5,
// body 1. A page's score is that sum times the number of query words it
// matched, so covering more of the query beats one strong field. The whole
// query appearing verbatim in a field earns that field's weight once more.
// A page must match at least half of the query words to be returned, so one
// shared word out of three is not a hit (wiki-mcp-docker#43).
func (s *Store) Search(domain, query string) ([]Page, error) {
	pages, err := s.ListPages(domain)
	if err != nil {
		return nil, err
	}
	phrase := strings.ToLower(strings.TrimSpace(query))
	terms := queryTerms(phrase)
	if len(terms) == 0 {
		return []Page{}, nil
	}
	type scored struct {
		page  Page
		score int
	}
	hits := []scored{}
	for _, p := range pages {
		f := searchFieldsOf(p)
		sum, matched := 0, 0
		for _, t := range terms {
			if w := f.weight(t); w > 0 {
				sum += w
				matched++
			}
		}
		if 2*matched < len(terms) {
			continue
		}
		score := sum * matched
		if len(terms) > 1 || phrase != terms[0] {
			score += f.weight(phrase)
		}
		hits = append(hits, scored{page: p, score: score})
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

// Field weights for Search.
const (
	weightAlias = 10
	weightSlug  = 8
	weightTitle = 8
	weightTag   = 5
	weightBody  = 1
)

// stopWords are dropped from multi-word queries: they occur in nearly every
// page body, so matching them would return the whole domain.
var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "does": true, "for": true, "from": true, "has": true,
	"how": true, "in": true, "into": true, "is": true, "it": true, "its": true,
	"of": true, "on": true, "or": true, "that": true, "the": true, "this": true,
	"to": true, "was": true, "what": true, "when": true, "where": true,
	"which": true, "who": true, "why": true, "with": true,
}

// queryTerms splits a lowercased query into distinct search words, dropping
// stop words and one-letter words unless nothing else is left.
func queryTerms(q string) []string {
	words := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var kept, all []string
	seen := map[string]bool{}
	for _, w := range words {
		if seen[w] {
			continue
		}
		seen[w] = true
		all = append(all, w)
		if len([]rune(w)) > 1 && !stopWords[w] {
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		return all
	}
	return kept
}

// searchFields is a page's searchable text, lowercased once per Search.
type searchFields struct {
	aliases, tags     []string
	slug, title, body string
}

func searchFieldsOf(p Page) searchFields {
	f := searchFields{
		slug:  strings.ToLower(p.Slug),
		title: strings.ToLower(pageTitle(p.Body)),
		body:  strings.ToLower(p.Body),
	}
	for _, a := range p.Aliases {
		f.aliases = append(f.aliases, strings.ToLower(a))
	}
	for _, t := range p.Tags {
		f.tags = append(f.tags, strings.ToLower(t))
	}
	return f
}

// weight is the sum of the weights of the fields that contain t.
func (f searchFields) weight(t string) int {
	w := 0
	if anyContains(f.aliases, t) {
		w += weightAlias
	}
	if strings.Contains(f.slug, t) {
		w += weightSlug
	}
	if f.title != "" && strings.Contains(f.title, t) {
		w += weightTitle
	}
	if anyContains(f.tags, t) {
		w += weightTag
	}
	if strings.Contains(f.body, t) {
		w += weightBody
	}
	return w
}

func anyContains(list []string, t string) bool {
	for _, s := range list {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

// pageTitle is the text of the body's first level-1 heading, or "".
func pageTitle(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(line[2:])
		}
	}
	return ""
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
		target := linkTarget(m[1])
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
			target := linkTarget(m[1])
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
				// Name-form link: never resolves (see DanglingInBody).
				if !isRawRef(target) {
					if dangling[target] == nil {
						dangling[target] = make(map[string]bool)
					}
					dangling[target][p.Slug] = true
				}
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

// linkTarget cleans a captured wikilink target. Inside a markdown table
// the display pipe is escaped ([[slug\|Display]]), so the capture ends in
// a backslash that belongs to the table syntax, not the slug.
func linkTarget(raw string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), `\`))
}

// isRawRef reports whether a wikilink target points at a raw source file
// ([[../../raw/sources/fiction/x.odt]]) rather than a wiki page. Those are
// not page links, so the dangling checks leave them alone.
func isRawRef(target string) bool {
	return strings.HasPrefix(target, "raw/") || strings.Contains(target, "/raw/")
}

// DanglingInBody scans the given page body for [[wikilink]] targets
// and returns those that don't resolve to any existing page in any
// domain. Same resolution semantics as Neighbors and Audit (in-domain
// first, then cross-domain fallback). Used by wiki_write to surface
// new broken references at the moment of the write — the model sees
// the deltas in its own action's response, no separate audit call
// needed.
func (s *Store) DanglingInBody(domain, body string) ([]string, error) {
	otherDomains, err := s.ListDomains()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	dangling := []string{}
	for _, m := range wikilinkRe.FindAllStringSubmatch(body, -1) {
		target := linkTarget(m[1])
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
			// A name-form link ([[Jen Chan]]) can never resolve; report it
			// verbatim so the writer fixes it (llm-wiki#61).
			if !isRawRef(target) && !seen[target] {
				seen[target] = true
				dangling = append(dangling, target)
			}
			continue
		}
		if seen[linkSlug] {
			continue
		}
		seen[linkSlug] = true

		if _, err := s.Lookup(domain, linkSlug); err == nil {
			continue
		}
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
		if !resolved {
			dangling = append(dangling, linkSlug)
		}
	}
	sort.Strings(dangling)
	return dangling, nil
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
