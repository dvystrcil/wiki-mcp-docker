# Two-target build for wiki-mcp.
#
#   prod   (default)  →  FROM scratch — static Go binary only.
#   debug             →  FROM alpine — adds curl/bash for incident triage.
#
# Why scratch is correct here:
#   - Pure Go logic. CGO_ENABLED=0 compiles statically.
#   - Final image ~6 MB.
#
# scratch has no root store. wiki_import POSTs to the n8n webhook over
# HTTPS, so the prod stage must carry a CA bundle or every import fails
# with "x509: certificate signed by unknown authority" — which is exactly
# what happened between v0.1.3 (when wiki_import landed) and this change.
#
# The wiki tree itself is NOT baked into the image; the deploy pod
# mounts /data/wiki/current (populated by git-sync) and points
# wiki-mcp at it via WIKI_ROOT.

FROM golang:1.26-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags='-s -w' -trimpath \
    -o /out/wiki-mcp ./cmd/wiki-mcp
# Minimal passwd/group so the scratch image can USER-switch.
# uid 10001 matches the deploy manifest's runAsUser AND the git-sync
# sidecar's fsGroup so the shared mount is writable.
RUN echo 'app:x:10001:10001::/:/sbin/nologin' > /out/passwd && \
    echo 'app:x:10001:' > /out/group

FROM scratch AS prod
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/passwd /etc/passwd
COPY --from=build /out/group /etc/group
COPY --from=build /out/wiki-mcp /usr/local/bin/wiki-mcp
USER app
EXPOSE 8080
ENV HTTP_ADDR=:8080
ENTRYPOINT ["/usr/local/bin/wiki-mcp"]

# Debug target — alpine + curl for incident triage.
FROM alpine:3 AS debug
RUN apk add --no-cache ca-certificates curl bash && \
    addgroup -S app && adduser -S -G app -u 10001 app
COPY --from=build /out/wiki-mcp /usr/local/bin/wiki-mcp
USER app
EXPOSE 8080
ENV HTTP_ADDR=:8080
ENTRYPOINT ["/usr/local/bin/wiki-mcp"]
