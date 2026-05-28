// Package mcpsrv wires the wiki-mcp tools into an MCP server.
package mcpsrv

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dvystrcil/wiki-mcp-docker/internal/wiki"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterAll attaches every wiki-mcp tool to the given server.
func RegisterAll(server *mcp.Server, store *wiki.Store) []string {
	names := []string{}
	for _, t := range []struct {
		name string
		add  func(*mcp.Server, *wiki.Store)
	}{
		{"wiki_lookup", addLookup},
		{"wiki_search", addSearch},
		{"wiki_neighbors", addNeighbors},
		{"wiki_write", addWrite},
		{"wiki_list_domains", addListDomains},
		{"wiki_import", addImport},
	} {
		t.add(server, store)
		names = append(names, t.name)
	}
	return names
}

// ---- helpers ----

func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("marshal result: %v", err))
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: s}},
	}
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

func decodeArgs(req *mcp.CallToolRequest, dst any) *mcp.CallToolResult {
	if len(req.Params.Arguments) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Params.Arguments, dst); err != nil {
		return errorResult(fmt.Sprintf("decode arguments: %v", err))
	}
	return nil
}

// pageSummary renders a Page in JSON-friendly form for responses.
func pageSummary(p wiki.Page, includeBody bool) map[string]any {
	out := map[string]any{
		"domain":   p.Domain,
		"type":     p.Type,
		"slug":     p.Slug,
		"aliases":  p.Aliases,
		"tags":     p.Tags,
	}
	if includeBody {
		out["body"] = p.Body
		out["frontmatter"] = p.Frontmatter
	}
	return out
}

// ---- tool: wiki_lookup ----

func addLookup(server *mcp.Server, store *wiki.Store) {
	tool := &mcp.Tool{
		Name: "wiki_lookup",
		Description: "Fetch a single wiki page by its (domain, slug). Returns the page's frontmatter, body, and metadata. " +
			"Use this when you know the exact page you need. For finding pages by content/keywords, use wiki_search instead.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"domain": {Type: "string", Description: "Wiki domain, e.g. \"one-ring\" or \"homelab\"."},
				"slug":   {Type: "string", Description: "Page slug (filename without .md), e.g. \"del\" or \"mcp-client\"."},
			},
			Required: []string{"domain", "slug"},
		},
	}
	server.AddTool(tool, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Domain string `json:"domain"`
			Slug   string `json:"slug"`
		}
		if e := decodeArgs(req, &args); e != nil {
			return e, nil
		}
		p, err := store.Lookup(args.Domain, args.Slug)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(pageSummary(p, true)), nil
	})
}

// ---- tool: wiki_search ----

func addSearch(server *mcp.Server, store *wiki.Store) {
	tool := &mcp.Tool{
		Name: "wiki_search",
		Description: "Search a domain's wiki pages by keyword. Matches against aliases, tags, slug, and body — case-insensitive. " +
			"Returns pages sorted by relevance (alias/tag hits > slug hits > body hits). Returns the metadata of each hit; " +
			"call wiki_lookup to retrieve full page bodies.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"domain": {Type: "string", Description: "Wiki domain to search within."},
				"query":  {Type: "string", Description: "Keyword or phrase to match."},
			},
			Required: []string{"domain", "query"},
		},
	}
	server.AddTool(tool, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Domain string `json:"domain"`
			Query  string `json:"query"`
		}
		if e := decodeArgs(req, &args); e != nil {
			return e, nil
		}
		pages, err := store.Search(args.Domain, args.Query)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		results := make([]map[string]any, len(pages))
		for i, p := range pages {
			results[i] = pageSummary(p, false)
		}
		return jsonResult(map[string]any{
			"domain":  args.Domain,
			"query":   args.Query,
			"hits":    len(results),
			"results": results,
		}), nil
	})
}

// ---- tool: wiki_neighbors ----

func addNeighbors(server *mcp.Server, store *wiki.Store) {
	tool := &mcp.Tool{
		Name: "wiki_neighbors",
		Description: "Return all pages directly linked from the given page via [[wikilink]] syntax. Handles in-domain, " +
			"cross-section, and cross-domain links. Only RESOLVED links appear (broken wikilinks are silently skipped).",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"domain": {Type: "string"},
				"slug":   {Type: "string"},
			},
			Required: []string{"domain", "slug"},
		},
	}
	server.AddTool(tool, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Domain string `json:"domain"`
			Slug   string `json:"slug"`
		}
		if e := decodeArgs(req, &args); e != nil {
			return e, nil
		}
		neighbors, err := store.Neighbors(args.Domain, args.Slug)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		results := make([]map[string]any, len(neighbors))
		for i, p := range neighbors {
			results[i] = pageSummary(p, false)
		}
		return jsonResult(map[string]any{
			"from":      map[string]string{"domain": args.Domain, "slug": args.Slug},
			"neighbors": results,
		}), nil
	})
}

// ---- tool: wiki_write ----

func addWrite(server *mcp.Server, store *wiki.Store) {
	tool := &mcp.Tool{
		Name: "wiki_write",
		Description: "Create or overwrite a wiki page. Writes to the local mount; a git-sync sidecar in the deploy pod " +
			"commits + pushes on a separate schedule. WILL REFUSE: writes under raw/ (immutable per AGENTS.md), invalid " +
			"domain/slug (must be lowercase-kebab), or invalid type (must be entities/concepts/sources/syntheses). " +
			"Provide the FULL page body including the YAML frontmatter.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"domain": {Type: "string", Description: "Target domain, e.g. \"one-ring\"."},
				"type":   {Type: "string", Description: "Page type directory: entities, concepts, sources, or syntheses.", Enum: []any{"entities", "concepts", "sources", "syntheses"}},
				"slug":   {Type: "string", Description: "Slug (filename without .md). Must be lowercase-kebab."},
				"body":   {Type: "string", Description: "Full page content including YAML frontmatter at the top."},
			},
			Required: []string{"domain", "type", "slug", "body"},
		},
	}
	server.AddTool(tool, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Domain string `json:"domain"`
			Type   string `json:"type"`
			Slug   string `json:"slug"`
			Body   string `json:"body"`
		}
		if e := decodeArgs(req, &args); e != nil {
			return e, nil
		}
		if err := store.Write(args.Domain, args.Type, args.Slug, args.Body); err != nil {
			return errorResult(err.Error()), nil
		}
		return textResult(fmt.Sprintf("wrote wiki/%s/%s/%s.md (%d bytes)", args.Domain, args.Type, args.Slug, len(args.Body))), nil
	})
}

// ---- tool: wiki_list_domains ----

func addListDomains(server *mcp.Server, store *wiki.Store) {
	tool := &mcp.Tool{
		Name:        "wiki_list_domains",
		Description: "List all domains in the wiki (one-ring, homelab, fiction, etc.). Useful when the caller doesn't know which domain a topic lives in.",
		InputSchema: &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}},
	}
	server.AddTool(tool, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		domains, err := store.ListDomains()
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(map[string]any{"domains": domains}), nil
	})
}
