# sgnl-mcp: one static binary on a minimal, non-root base image.
# The build stage runs on the builder's own platform and cross-compiles for
# the target one (linux/amd64, linux/arm64): no emulation needed.
FROM --platform=$BUILDPLATFORM golang:1.27.4 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /sgnl-mcp ./cmd/sgnl-mcp
# License texts of everything compiled into the binary (Go and each module), shipped in
# /licenses. The build fails when a module has no license file.
RUN set -e; mkdir -p /licenses/go; cp "$(go env GOROOT)/LICENSE" /licenses/go/; \
    [ -f "$(go env GOROOT)/PATENTS" ] && cp "$(go env GOROOT)/PATENTS" /licenses/go/; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go list -deps -f '{{with .Module}}{{.Path}} {{.Dir}}{{end}}' ./cmd/sgnl-mcp | sort -u | \
    while read -r mod dir; do \
      mkdir -p "/licenses/$mod"; \
      for f in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* "$dir"/NOTICE* "$dir"/PATENTS*; do \
        [ -f "$f" ] && cp "$f" "/licenses/$mod/"; \
      done; \
      ls "/licenses/$mod" | grep -q . || { echo "no license file: $mod" >&2; exit 1; }; \
    done

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /sgnl-mcp /sgnl-mcp
COPY --from=build /licenses /licenses
ENV SGNL_MCP_ADDR=0.0.0.0:8765 SGNL_MCP_LOG=json
EXPOSE 8765
USER nonroot
ENTRYPOINT ["/sgnl-mcp"]
