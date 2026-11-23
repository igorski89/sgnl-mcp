# sgnl-mcp: one static binary on a minimal, non-root base image.
FROM golang:1.27.4 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /sgnl-mcp ./cmd/sgnl-mcp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /sgnl-mcp /sgnl-mcp
ENV SGNL_MCP_ADDR=0.0.0.0:8765 SGNL_MCP_LOG=json
EXPOSE 8765
USER nonroot
ENTRYPOINT ["/sgnl-mcp"]
