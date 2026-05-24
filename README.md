# wiki-mcp

Go MCP server exposing the [llm-wiki](https://github.com/dvystrcil/llm-wiki) layout
(multi-domain markdown wiki, Karpathy-style) as MCP tools.

Phase 3 of dvystrcil/homelab#211.

## Tools

| Tool                | Purpose                                                  |
|---------------------|----------------------------------------------------------|
| `wiki_list_domains` | List domains (one-ring, homelab, fiction, …).            |
| `wiki_lookup`       | Fetch one page by (domain, slug).                        |
| `wiki_search`       | Case-insensitive search across body + frontmatter.       |
| `wiki_neighbors`    | Pages directly linked via `[[wikilink]]`.                |
| `wiki_write`        | Create / overwrite a page. Refuses to touch `raw/`.      |

Search scoring: alias hit = 10, slug hit = 8, tag hit = 5, body hit = 1.

## Storage

Filesystem only. The deploy pod runs a git-sync sidecar that clones
`dvystrcil/llm-wiki` onto a shared volume and pushes back on a schedule.
This server reads + writes to the local mount only — no git, no network
egress from this binary.

`WIKI_ROOT` (env or `--wiki-root` flag) points at the `wiki/` directory
inside the cloned repo. Default: `/data/wiki/current/wiki`.

## Transports

| Mode             | When                                  |
|------------------|---------------------------------------|
| Streamable HTTP  | In-cluster pod (`HTTP_ADDR=:8080`).   |
| Stdio            | Local dev / Claude Code over a tunnel.|

`/healthz` on the HTTP listener is a separate handler so kubelet probes
don't trip over MCP-protocol checks.

## Local development

```sh
go test ./...
go run ./cmd/wiki-mcp --wiki-root /path/to/llm-wiki/wiki --http :8080
```

## Build

The image is built in-cluster by the `wiki-mcp-docker-runner` ARC scale
set and pushed to `harbor-core/ai/wiki-mcp`. See `.github/workflows/build.yaml`.

## Deploy

Manifests live in [dvystrcil/wiki-mcp](https://github.com/dvystrcil/wiki-mcp);
the ArgoCD `Application` is in `dvystrcil/argocd-projects`. This follows the
[three-repo split](https://github.com/dvystrcil/homelab#211) for cluster
services: `-docker` (build) + bare name (deploy) + argocd-projects (Argo App).

## License

MIT. See [LICENSE](LICENSE).
