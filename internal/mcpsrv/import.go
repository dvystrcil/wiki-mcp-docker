package mcpsrv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/dvystrcil/wiki-mcp-docker/internal/wiki"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- tool: wiki_import ----
//
// Imports a Tolkien Gateway page (or other external source) into the wiki
// ON DEMAND, by delegating to an n8n webhook which dispatches a GH Actions
// workflow (scripts/import_tolkien_gateway.py) and waits for its result.
//
// This tool is the model's escape hatch for the "wiki_search returned nothing
// but the player named what looks like a canonical Middle-earth place" case.
// Without this, the Loremaster system prompt's "do not invent geography"
// rule leaves the model with no recourse but to bail out of the scene.
//
// Behavior is synchronous from the model's perspective: the n8n webhook
// blocks until the GH Actions run completes (or times out), then returns
// a structured response describing what happened.
//
// Env config:
//   N8N_TG_IMPORT_URL    — n8n webhook URL (default: prod URL below)
//   N8N_TG_IMPORT_TOKEN  — bearer token, sent as Authorization header
//                          when set; missing is permitted for dev/test
//
// Tracked: dvystrcil/homelab#240.

const (
	defaultImportURL = "https://n8n.sirddail.net/webhook/tg-import"

	// GH Actions cold-start + checkout + import + commit + push fits well
	// within 60s. 90s gives headroom for runner warm-up and TG slowness.
	importTimeout = 90 * time.Second

	// After n8n reports "imported", the file is on GitHub but our local
	// /data volume only updates when the git-sync sidecar pulls. Poll
	// the local store with this budget so callers see the page locally
	// before the tool returns. Budget = git-sync interval + headroom.
	localPollBudget   = 35 * time.Second
	localPollInterval = 1 * time.Second
)

// importRequest is the JSON we POST to the n8n webhook.
type importRequest struct {
	Term   string `json:"term"`
	Slug   string `json:"slug,omitempty"`
	Domain string `json:"domain,omitempty"`
	Type   string `json:"type,omitempty"`
}

// importResponse is what n8n returns to us — and what we surface to the
// model. The four statuses are documented in the tool description.
type importResponse struct {
	Status  string `json:"status"`            // imported | not_found | disambiguation | error
	Slug    string `json:"slug,omitempty"`    // local slug on imported
	Tried   string `json:"tried,omitempty"`   // term we asked for (echoed back)
	Message string `json:"message,omitempty"` // human-readable detail / suggestions / option list
}

func addImport(server *mcp.Server, store *wiki.Store) {
	tool := &mcp.Tool{
		Name: "wiki_import",
		Description: "Import a canonical Middle-earth place into the wiki ON DEMAND from Tolkien Gateway. " +
			"Use this when wiki_search(domain=\"one-ring\", query=<place>) returns no hits for what looks like " +
			"a real Tolkien place: call wiki_import with the term, then call wiki_lookup with the returned slug. " +
			"\n\nReturns one of five statuses (read the `status` field and act accordingly):\n" +
			"  - `imported`: page added AND visible locally; call wiki_lookup(domain=\"one-ring\", slug=<returned slug>) right away.\n" +
			"  - `imported_pending`: page added to the remote wiki but the local cache has not synced yet. Wait ~30s and call wiki_lookup. Do NOT name canonical geography for this place until the lookup succeeds.\n" +
			"  - `not_found`: Tolkien Gateway has no page for this term. The place is non-canonical OR misspelled. " +
			"DO NOT proceed to name distances/directions; ask the player to clarify or describe vaguely.\n" +
			"  - `disambiguation`: multiple TG pages match. Ask the player which they meant, then re-call wiki_import.\n" +
			"  - `error`: import pipeline failed (network/script). Describe the journey vaguely without naming canonical geography.\n" +
			"\nDO NOT call this tool for the same term twice in the same session — check your tool history first. " +
			"DO NOT call this for places that are obviously player-invented (the player's hometown, etc.).",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"term": {
					Type:        "string",
					Description: "Tolkien Gateway page name (e.g. \"Dunland\", \"Lonely Mountain\", \"Weathertop\"). Match TG's capitalization when possible.",
				},
				"slug": {
					Type:        "string",
					Description: "Optional local slug override. Default: slugify(term).",
				},
				"domain": {
					Type:        "string",
					Description: "Wiki domain to write into. Default: \"one-ring\".",
				},
				"type": {
					Type:        "string",
					Description: "Wiki entry type. Default: \"entity\". One of: entity, concept, source, synthesis.",
					Enum:        []any{"entity", "concept", "source", "synthesis"},
				},
			},
			Required: []string{"term"},
		},
	}

	server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args importRequest
		if e := decodeArgs(req, &args); e != nil {
			return e, nil
		}
		if args.Term == "" {
			return errorResult("term is required and must be non-empty"), nil
		}
		// Apply defaults — matches the workflow's defaults too, but we
		// echo them explicitly so the n8n side doesn't need to fill in.
		if args.Domain == "" {
			args.Domain = "one-ring"
		}
		if args.Type == "" {
			args.Type = "entity"
		}

		url := getenvOr("N8N_TG_IMPORT_URL", defaultImportURL)
		token := os.Getenv("N8N_TG_IMPORT_TOKEN")

		payload, err := json.Marshal(args)
		if err != nil {
			return errorResult(fmt.Sprintf("marshal request: %v", err)), nil
		}

		// Per-call context timeout so a stuck n8n call can't pin the
		// MCP connection forever.
		callCtx, cancel := context.WithTimeout(ctx, importTimeout)
		defer cancel()

		httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return errorResult(fmt.Sprintf("build request: %v", err)), nil
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")
		if token != "" {
			httpReq.Header.Set("Authorization", "Bearer "+token)
		}

		client := &http.Client{Timeout: importTimeout}
		resp, err := client.Do(httpReq)
		if err != nil {
			// Network failure — surface as `error` so the model knows
			// to describe vaguely rather than invent.
			return jsonResult(importResponse{
				Status:  "error",
				Tried:   args.Term,
				Message: fmt.Sprintf("n8n webhook unreachable: %v", err),
			}), nil
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			return jsonResult(importResponse{
				Status:  "error",
				Tried:   args.Term,
				Message: fmt.Sprintf("n8n returned HTTP %d", resp.StatusCode),
			}), nil
		}

		var out importResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return jsonResult(importResponse{
				Status:  "error",
				Tried:   args.Term,
				Message: fmt.Sprintf("decode n8n response: %v", err),
			}), nil
		}
		// Defensive: n8n may not always echo `tried`; backfill so the
		// model always knows what term we acted on.
		if out.Tried == "" {
			out.Tried = args.Term
		}

		// If n8n reported a successful import, poll the local store until
		// the git-sync sidecar pulls the new page (so the immediate
		// wiki_lookup call works). If polling times out, downgrade the
		// status so the model knows the page isn't visible yet and can
		// retry rather than fail mysteriously.
		if out.Status == "imported" && out.Slug != "" {
			deadline := time.Now().Add(localPollBudget)
			for time.Now().Before(deadline) {
				if _, err := store.Lookup(args.Domain, out.Slug); err == nil {
					// Page visible locally. Annotate the message so the
					// model sees that wiki_lookup will succeed now.
					out.Message = out.Message + " (locally visible)"
					return jsonResult(out), nil
				}
				select {
				case <-callCtx.Done():
					out.Status = "imported_pending"
					out.Message = "Import succeeded on remote but local view did not update before request was cancelled. Wait ~30s and call wiki_lookup again."
					return jsonResult(out), nil
				case <-time.After(localPollInterval):
				}
			}
			out.Status = "imported_pending"
			out.Message = fmt.Sprintf("Import succeeded on remote (slug=%s) but local /data has not synced yet. Wait ~30s and call wiki_lookup again — the git-sync sidecar pulls on a schedule.", out.Slug)
		}

		return jsonResult(out), nil
	})
}

func getenvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
