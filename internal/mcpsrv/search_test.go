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

// AC3 of wiki-mcp-docker#43, as corrected in the issue comment: a query
// whose words appear in no page gives zero hits and keeps the import hint.
// The AC's literal 'nonexistent river source' would now (rightly) find
// brandywine-river through 'river', so 'mountain' stands in for it. Stop
// words alone must not match either.
func TestSearchNoFalsePositives(t *testing.T) {
	sess := sessionFor(t, oneRingWithBrandywine(t))
	for _, q := range []string{"nonexistent mountain source", "the nonexistent of a"} {
		out := callSearch(t, sess, "one-ring", q)
		if got := out["hits"]; got != float64(0) {
			t.Errorf("wiki_search(one-ring, %q) hits = %v (%v), want 0", q, got, hitSlugs(out))
		}
		if _, ok := out["hint"]; !ok {
			t.Errorf("zero-hit search %q lost its wiki_import hint", q)
		}
	}
}
