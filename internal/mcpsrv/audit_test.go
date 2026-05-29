package mcpsrv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dvystrcil/wiki-mcp-docker/internal/wiki"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// End-to-end test for wiki_audit: spins a real MCP server with the tool
// registered, dials it with a real client over Streamable HTTP, and
// verifies the audit report shape from a fixture wiki tree.
func TestWikiAudit_EndToEnd(t *testing.T) {
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

	argsJSON, _ := json.Marshal(map[string]any{"domain": "fiction"})
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "wiki_audit",
		Arguments: json.RawMessage(argsJSON),
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("wiki_audit error: %+v", res.Content)
	}
	var body string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			body = tc.Text
			break
		}
	}
	if !strings.Contains(body, `"domain": "fiction"`) {
		t.Errorf("report missing fiction domain:\n%s", body)
	}
	if !strings.Contains(body, `"orphans"`) || !strings.Contains(body, `"dangling"`) {
		t.Errorf("report missing orphans/dangling fields:\n%s", body)
	}
}

// wikiFixture builds a small wiki tree with one fiction entity whose
// `## Related` references a missing slug (so the audit sees both an
// orphan and a dangling entry).
func wikiFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"fiction/entities", "fiction/concepts", "fiction/sources", "fiction/syntheses"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	page := `---
type: entity
domain: fiction
aliases: ["Alpha"]
tags: [character]
created: 2026-05-29
updated: 2026-05-29
source_count: 0
author_validated: false
---

# Alpha

## Identity

A character.

## Related

- [[beta]]
`
	if err := os.WriteFile(filepath.Join(root, "fiction/entities/alpha.md"), []byte(page), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return root
}
