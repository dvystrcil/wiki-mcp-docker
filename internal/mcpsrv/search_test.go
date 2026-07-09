package mcpsrv

import (
	"encoding/json"
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
