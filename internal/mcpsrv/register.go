// Package mcpsrv wires the wiki-mcp tools into an MCP server.
package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
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
		{"wiki_audit", addAudit},
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
		"domain":  p.Domain,
		"type":    p.Type,
		"slug":    p.Slug,
		"aliases": p.Aliases,
		"tags":    p.Tags,
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
			if errors.Is(err, wiki.ErrPageNotFound) {
				return errorResult(notFoundMessage(args.Domain, args.Slug, err)), nil
			}
			return errorResult(err.Error()), nil
		}
		return jsonResult(pageSummary(p, true)), nil
	})
}

// importableDomains are the wiki domains wiki_import can actually fill.
// wiki_import sources from Tolkien Gateway, so naming it as a recovery path
// for any other domain would point the model at a tool that cannot serve it.
var importableDomains = map[string]bool{"one-ring": true}

// notFoundMessage renders wiki_lookup's not-found error and, where an import
// path exists, names wiki_import inline. Stating the escape hatch in
// wiki_import's own description was not enough to get the model to reach for
// it (homelab#312: zero calls across 40 prompts), so the recovery path has to
// travel in the response the model is already reading.
func notFoundMessage(domain, slug string, err error) string {
	if !importableDomains[domain] {
		return err.Error()
	}
	return fmt.Sprintf(
		"%v.\n\n"+
			"If %q is a real, canonical Tolkien term, your next action should be "+
			"wiki_import(domain=%q, term=%q). That fetches it from Tolkien Gateway and "+
			"stores it, so this lookup succeeds now and in every later conversation.\n\n"+
			"Do NOT answer from training data without calling wiki_import first. "+
			"If wiki_import returns not_found, the term is non-canonical or misspelled — "+
			"say so plainly rather than inventing detail.",
		err, slug, domain, slug)
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
		out := map[string]any{
			"domain":  args.Domain,
			"query":   args.Query,
			"hits":    len(results),
			"results": results,
		}
		if len(results) == 0 && importableDomains[args.Domain] {
			out["hint"] = zeroHitHint(args.Domain, args.Query)
		}
		return jsonResult(out), nil
	})
}

// zeroHitHint names wiki_import on an empty search result. A zero-hit search
// is where the model actually gets stuck — 8 of 9 dead ends across the AC4a
// eval runs, against 1 for wiki_lookup's not-found. Saying so in the tool
// description and the system prompt did not move it; this puts the recovery
// path in the result the model is reading.
//
// The query is often a phrase ("Grey Havens Mithlond to Rivendell road"),
// not a page name, so the hint asks for the place name rather than echoing
// the query into term=.
func zeroHitHint(domain, query string) string {
	return fmt.Sprintf(
		"No wiki page matched %q. If that query names a real, canonical Tolkien "+
			"place, call wiki_import(domain=%q, term=\"<the place name>\") now — "+
			"pass the place itself (e.g. \"Fornost\"), not the whole query — then "+
			"wiki_lookup the slug it returns.\n\n"+
			"Do NOT answer from training data without trying wiki_import first. "+
			"If wiki_import returns not_found, the place is non-canonical or "+
			"misspelled — say so plainly rather than inventing detail.",
		query, domain)
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
			"Provide the FULL page body including the YAML frontmatter. " +
			"The response includes a `dangling` list — any [[wikilink]] in the body you just wrote that doesn't " +
			"resolve to an existing page in any domain. Each entry is either a target you should write next " +
			"(offer the author a stub) or a slug typo / speculative reference that should be pruned (offer " +
			"the author the prune). Resolve before moving on so the link graph stays clean.",
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
		// Surface dangling [[wikilinks]] in the just-written body so the
		// model sees the deltas inside its own action's response — no
		// separate audit call needed.
		dangling, _ := store.DanglingInBody(args.Domain, args.Body)
		return jsonResult(map[string]any{
			"path":     fmt.Sprintf("wiki/%s/%s/%s.md", args.Domain, args.Type, args.Slug),
			"bytes":    len(args.Body),
			"dangling": dangling,
		}), nil
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

// ---- tool: wiki_audit ----

func addAudit(server *mcp.Server, store *wiki.Store) {
	tool := &mcp.Tool{
		Name: "wiki_audit",
		Description: "Audit the link-graph health of a domain. Returns hubs (most-linked-to pages), " +
			"orphans (pages no one links to), and dangling wikilink targets that don't resolve to any " +
			"existing page in any domain. Reports state; does NOT mutate. Call this at the end of a " +
			"writing session — orphans are entities to weave in, dangling targets are entities to write " +
			"next (or links to remove if the target was speculative).",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"domain": {Type: "string", Description: "Domain to audit (e.g., fiction, one-ring, homelab)."},
			},
			Required: []string{"domain"},
		},
	}
	server.AddTool(tool, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Domain string `json:"domain"`
		}
		if e := decodeArgs(req, &args); e != nil {
			return e, nil
		}
		report, err := store.Audit(args.Domain)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(report), nil
	})
}
