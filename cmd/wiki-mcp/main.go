// wiki-mcp — Go MCP server for the llm-wiki layout.
//
// Phase 3 of dvystrcil/homelab#211. Exposes wiki_lookup / wiki_search /
// wiki_neighbors / wiki_write / wiki_list_domains so OWUI presets and
// other MCP clients can read + write the wiki during chats.
//
// Storage: filesystem. The deploy pod has a git-sync sidecar that
// clones dvystrcil/llm-wiki onto a shared volume and pushes back on
// a schedule. This server only reads from + writes to the mount.
//
// Streamable-HTTP for in-cluster use; stdio fallback for Claude Code
// over a tunnel. /healthz is separate so kubelet probes don't trip
// over the MCP handler.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/dvystrcil/wiki-mcp-docker/internal/mcpsrv"
	"github.com/dvystrcil/wiki-mcp-docker/internal/wiki"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	wikiRoot := flag.String("wiki-root",
		envOr("WIKI_ROOT", "/data/wiki/current/wiki"),
		"Filesystem path to the wiki/ directory (containing per-domain subdirs).")
	httpAddr := flag.String("http",
		envOr("HTTP_ADDR", ""),
		"If set, serve MCP over HTTP at this address (e.g. ':8080'). Otherwise stdio.")
	flag.Parse()

	log.Printf("Starting wiki-mcp")
	log.Printf("Wiki root: %s", *wikiRoot)

	store, err := wiki.NewStore(*wikiRoot)
	if err != nil {
		log.Fatalf("open wiki store: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "wiki-mcp",
		Version: "0.1.0",
	}, nil)

	registered := mcpsrv.RegisterAll(server, store)
	log.Printf("Registered %d tool(s): %v", len(registered), registered)

	if *httpAddr != "" {
		mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return server
		}, nil)

		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
		})
		mux.Handle("/", mcpHandler)

		log.Printf("Listening on HTTP %s (streamable transport, /healthz live)", *httpAddr)
		if err := http.ListenAndServe(*httpAddr, mux); err != nil {
			log.Fatalf("http server: %v", err)
		}
		return
	}

	log.Printf("Listening on stdio")
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
