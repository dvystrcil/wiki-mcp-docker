package mcpsrv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dvystrcil/wiki-mcp-docker/internal/wiki"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// lookupSession spins a real MCP server over Streamable HTTP against a
// fixture wiki tree and returns a connected session.
func lookupSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	root := wikiFixture(t)
	store, err := wiki.NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name: "wiki-mcp-test", Version: "v0.1.0",
	}, nil)
	RegisterAll(server, store)

	handler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{JSONResponse: true},
	)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)

	transport := &mcp.StreamableClientTransport{Endpoint: httpSrv.URL}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0.1.0"}, nil)
	sess, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func callLookup(t *testing.T, sess *mcp.ClientSession, domain, slug string) *mcp.CallToolResult {
	t.Helper()
	argsJSON, _ := json.Marshal(map[string]any{"domain": domain, "slug": slug})
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "wiki_lookup",
		Arguments: json.RawMessage(argsJSON),
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	return res
}

func resultText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// A not-found lookup in the one-ring domain must name wiki_import in its
// own response. The tool description alone did not get the model to reach
// for the escape hatch (homelab#312: zero wiki_import calls across 40
// prompts), so the recovery path has to travel in the payload the model
// is already reading.
func TestWikiLookup_NotFoundSuggestsImport(t *testing.T) {
	sess := lookupSession(t)
	res := callLookup(t, sess, "one-ring", "forochel")

	if !res.IsError {
		t.Fatalf("expected not-found to be an error result, got: %+v", res.Content)
	}
	body := resultText(res)
	if !strings.Contains(body, "wiki_import") {
		t.Errorf("not-found response does not name wiki_import:\n%s", body)
	}
	if !strings.Contains(body, "forochel") {
		t.Errorf("not-found response does not echo the slug:\n%s", body)
	}
}

// wiki_import fetches from Tolkien Gateway, so it can only serve the
// one-ring domain. Suggesting it for any other domain would point the
// model at a tool that cannot satisfy the request.
func TestWikiLookup_NotFoundDoesNotSuggestImportForOtherDomains(t *testing.T) {
	sess := lookupSession(t)
	res := callLookup(t, sess, "fiction", "nonexistent-page")

	if !res.IsError {
		t.Fatalf("expected not-found to be an error result, got: %+v", res.Content)
	}
	body := resultText(res)
	if strings.Contains(body, "wiki_import") {
		t.Errorf("wiki_import suggested for a domain it cannot serve:\n%s", body)
	}
}

// A malformed slug is a caller bug, not a missing page. Importing it would
// fail too, so the hint must not fire.
func TestWikiLookup_InvalidSlugDoesNotSuggestImport(t *testing.T) {
	sess := lookupSession(t)
	res := callLookup(t, sess, "one-ring", "Not A Slug")

	if !res.IsError {
		t.Fatalf("expected invalid slug to be an error result, got: %+v", res.Content)
	}
	body := resultText(res)
	if strings.Contains(body, "wiki_import") {
		t.Errorf("wiki_import suggested for an invalid slug:\n%s", body)
	}
}
