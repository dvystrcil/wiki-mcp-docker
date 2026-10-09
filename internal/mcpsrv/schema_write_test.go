package mcpsrv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dvystrcil/wiki-mcp-docker/internal/wiki"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// wiki-mcp-docker#1: wiki_write reports schema violations in its response,
// LENIENTLY -- the page is still written, like the `dangling` list. 19 of the
// 23 HIGH lint violations on llm-wiki main came from wiki_write writes that
// nothing flagged at the time.
const schemaJSON = `{"type_for_dir":{"entities":"entity","concepts":"concept","sources":"source","syntheses":"synthesis"},
"known_domains":["fiction"],"required_frontmatter":{"entity":["type","domain"]},
"default_sections":{"entity":["## Identity"]},"domain_sections":{"fiction":{"entity":["## Identity","## Related"]}},
"source_path_prefixes":["raw/sources/"]}`

func callWrite(t *testing.T, store *wiki.Store, body string) map[string]any {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "v0"}, nil)
	RegisterAll(server, store)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{JSONResponse: true}))
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	args, _ := json.Marshal(map[string]any{"domain": "fiction", "type": "entities", "slug": "zeta", "body": body})
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "wiki_write", Arguments: json.RawMessage(args)})
	if err != nil || res.IsError {
		t.Fatalf("wiki_write: err=%v res=%+v", err, res)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func storeWithSchema(t *testing.T) (*wiki.Store, string) {
	root := wikiFixture(t)
	store, err := wiki.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "schema.json")
	_ = os.WriteFile(p, []byte(schemaJSON), 0o644)
	s, err := wiki.LoadSchema(p)
	if err != nil {
		t.Fatal(err)
	}
	store.SetSchema(s)
	return store, root
}

func TestWikiWrite_ReportsSchemaViolationsButStillWrites(t *testing.T) {
	store, root := storeWithSchema(t)
	out := callWrite(t, store, "# Zeta\n\nprose with no frontmatter\n")
	if out["schema_checked"] != true {
		t.Fatalf("schema_checked = %v", out["schema_checked"])
	}
	vs, _ := out["schema_violations"].([]any)
	if len(vs) != 1 || vs[0].(map[string]any)["rule"] != "missing_frontmatter" {
		t.Fatalf("want one missing_frontmatter, got %v", out["schema_violations"])
	}
	if _, err := os.Stat(filepath.Join(root, "fiction", "entities", "zeta.md")); err != nil {
		t.Fatalf("lenient: the page must still be written: %v", err)
	}
}

func TestWikiWrite_ConformingPageHasEmptyViolations(t *testing.T) {
	store, _ := storeWithSchema(t)
	out := callWrite(t, store, "---\ntype: entity\ndomain: fiction\n---\n## Identity\n## Related\n")
	if vs, ok := out["schema_violations"].([]any); !ok || len(vs) != 0 {
		t.Fatalf("want [], got %#v", out["schema_violations"])
	}
}

func TestWikiWrite_WithoutSchemaSaysSoInsteadOfLookingClean(t *testing.T) {
	store, err := wiki.NewStore(wikiFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	out := callWrite(t, store, "no frontmatter at all")
	if out["schema_checked"] != false {
		t.Fatalf("an unchecked write must say so: schema_checked=%v", out["schema_checked"])
	}
}
