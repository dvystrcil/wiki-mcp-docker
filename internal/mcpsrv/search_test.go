package mcpsrv

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func callSearch(t *testing.T, sess *mcp.ClientSession, domain, query string) map[string]any {
	t.Helper()
	argsJSON, _ := json.Marshal(map[string]any{"domain": domain, "query": query})
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "wiki_search",
		Arguments: json.RawMessage(argsJSON),
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("wiki_search errored: %+v", res.Content)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("decode result: %v\n%s", err, resultText(res))
	}
	return out
}

// A zero-hit search in one-ring is the model's most common dead end
// (wiki-mcp-docker#8 AC4a: 8 of 9 dead ends across two eval runs). The
// import path has to be named here, not only on wiki_lookup's not-found.
func TestWikiSearch_ZeroHitSuggestsImport(t *testing.T) {
	sess := lookupSession(t)
	out := callSearch(t, sess, "one-ring", "definitely-not-a-real-place")

	if got := out["hits"]; got != float64(0) {
		t.Fatalf("expected 0 hits, got %v", got)
	}
	hint, ok := out["hint"].(string)
	if !ok {
		t.Fatalf("zero-hit search has no hint field: %v", out)
	}
	if !strings.Contains(hint, "wiki_import") {
		t.Errorf("hint does not name wiki_import: %q", hint)
	}
	if !strings.Contains(hint, "one-ring") {
		t.Errorf("hint does not name the domain: %q", hint)
	}
}

// A search that found something needs no recovery path.
func TestWikiSearch_HitsHaveNoHint(t *testing.T) {
	sess := lookupSession(t)
	out := callSearch(t, sess, "fiction", "Alpha")

	if out["hits"] == float64(0) {
		t.Fatalf("fixture should match; got 0 hits: %v", out)
	}
	if _, present := out["hint"]; present {
		t.Errorf("a search with hits must not carry an import hint: %v", out["hint"])
	}
}

// wiki_import sources from Tolkien Gateway. A zero-hit search in a domain
// it cannot fill must not point the model at it.
func TestWikiSearch_ZeroHitNoHintForOtherDomains(t *testing.T) {
	sess := lookupSession(t)
	out := callSearch(t, sess, "fiction", "definitely-not-a-real-place")

	if got := out["hits"]; got != float64(0) {
		t.Fatalf("expected 0 hits, got %v", got)
	}
	if _, present := out["hint"]; present {
		t.Errorf("wiki_import hinted for a domain it cannot serve: %v", out["hint"])
	}
}

// oneRingWithBrandywine is the one-ring shape from wiki-mcp-docker#43: the
// page exists with alias 'Brandywine', and a second page that shares none
// of the AC3 query words.
func oneRingWithBrandywine(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("one-ring/entities/brandywine-river.md", `---
type: entity
domain: one-ring
aliases: ["Brandywine River", "Brandywine"]
tags: [place]
---

# Brandywine River

Bounds the Shire to the east.
`)
	write("one-ring/entities/strider.md", `---
type: entity
domain: one-ring
aliases: ["Aragorn"]
---

# Strider

A Ranger of the North.
`)
	return root
}

func hitSlugs(out map[string]any) []string {
	results, _ := out["results"].([]any)
	var s []string
	for _, r := range results {
		m, _ := r.(map[string]any)
		s = append(s, fmt.Sprint(m["slug"]))
	}
	return s
}

// AC1 + AC2 of wiki-mcp-docker#43.
func TestSearchPartialAliasMatch(t *testing.T) {
	sess := sessionFor(t, oneRingWithBrandywine(t))
	for _, q := range []string{"Brandywine river source", "Brandywine source"} {
		out := callSearch(t, sess, "one-ring", q)
		got := hitSlugs(out)
		if len(got) == 0 || got[0] != "brandywine-river" {
			t.Errorf("wiki_search(one-ring, %q) = %v, want brandywine-river first", q, got)
		}
	}
}

// AC3 of wiki-mcp-docker#43: zero hits, with the import hint kept, for the
// AC's literal query. brandywine-river shares one word ('river') of three;
// a page has to match at least half of the query's words to count. Stop
// words alone must not match either.
func TestSearchNoFalsePositives(t *testing.T) {
	sess := sessionFor(t, oneRingWithBrandywine(t))
	for _, q := range []string{"nonexistent river source", "nonexistent mountain source", "the nonexistent of a"} {
		out := callSearch(t, sess, "one-ring", q)
		if got := out["hits"]; got != float64(0) {
			t.Errorf("wiki_search(one-ring, %q) hits = %v (%v), want 0", q, got, hitSlugs(out))
		}
		if _, ok := out["hint"]; !ok {
			t.Errorf("zero-hit search %q lost its wiki_import hint", q)
		}
	}
}

// writeWikiFile writes body to root/rel, creating directories.
func writeWikiFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The homelab repro from #43: no page is about DNS, and pages that share one
// generic word ('server') of six must not come back as hits.
func TestSearchMinorityWordMatchIsNotAHit(t *testing.T) {
	root := t.TempDir()
	writeWikiFile(t, root, "homelab/entities/mealie-mcp.md", "---\naliases: [\"Mealie MCP\"]\n---\n\n# Mealie MCP\n\nAn MCP server for recipes.\n")
	sess := sessionFor(t, root)
	out := callSearch(t, sess, "homelab", "name-lookup server DNS managed GitOps namespace")
	if got := out["hits"]; got != float64(0) {
		t.Errorf("hits = %v (%v), want 0", got, hitSlugs(out))
	}
}

func manyPages(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < n; i++ {
		writeWikiFile(t, root, fmt.Sprintf("fiction/entities/lamp-%02d.md", i), "# Lamp\n\nA lamp.\n")
	}
	return root
}

func callSearchArgs(t *testing.T, sess *mcp.ClientSession, args map[string]any) map[string]any {
	t.Helper()
	argsJSON, _ := json.Marshal(args)
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "wiki_search", Arguments: json.RawMessage(argsJSON)})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("wiki_search errored: %+v", res.Content)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// A broad query must not flood the model's context: by default at most 10
// results come back, 'hits' still gives the total, and 'truncated' says so.
func TestWikiSearch_DefaultLimit(t *testing.T) {
	sess := sessionFor(t, manyPages(t, 14))
	out := callSearch(t, sess, "fiction", "lamp")
	if got := len(hitSlugs(out)); got != 10 {
		t.Errorf("results = %d, want 10", got)
	}
	if out["hits"] != float64(14) {
		t.Errorf("hits = %v, want 14 (the total)", out["hits"])
	}
	if out["truncated"] != true {
		t.Errorf("truncated = %v, want true", out["truncated"])
	}
}

func TestWikiSearch_LimitParam(t *testing.T) {
	sess := sessionFor(t, manyPages(t, 14))
	out := callSearchArgs(t, sess, map[string]any{"domain": "fiction", "query": "lamp", "limit": 3})
	if got := len(hitSlugs(out)); got != 3 {
		t.Errorf("results = %d, want 3", got)
	}
	out = callSearchArgs(t, sess, map[string]any{"domain": "fiction", "query": "lamp", "limit": 500})
	if got := len(hitSlugs(out)); got != 14 {
		t.Errorf("limit above the total: results = %d, want 14", got)
	}
	if _, present := out["truncated"]; present {
		t.Errorf("nothing was cut, but truncated is set: %v", out["truncated"])
	}
}

// The cap has a ceiling so one call cannot ask for the whole domain.
func TestWikiSearch_LimitCeiling(t *testing.T) {
	sess := sessionFor(t, manyPages(t, 60))
	out := callSearchArgs(t, sess, map[string]any{"domain": "fiction", "query": "lamp", "limit": 1000})
	if got := len(hitSlugs(out)); got != 50 {
		t.Errorf("results = %d, want 50 (the ceiling)", got)
	}
}
