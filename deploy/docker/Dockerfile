# syntax=docker/dockerfile:1
#
# Multi-stage build for the bubbles server (linux/amd64 and linux/arm64).
#   1. web:   builds the Svelte SPA into internal/webui/dist (embedded by the server)
#   2. build: static Go binary, CGO_ENABLED=0, module path read from go.mod
#   3. final: distroless static, nonroot, data in /data
#
# The web and build stages run on the build host's platform and cross-compile
# for the target, so a multi-arch build runs npm and go without emulation.
#
# Build from the repository root:
#   docker buildx build -f deploy/docker/Dockerfile -t bubbles:dev --load .

ARG NODE_IMAGE=node:26.5.1-alpine
ARG GO_IMAGE=golang:1.26.9-alpine
ARG FINAL_IMAGE=gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev

FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS web
WORKDIR /src/web
COPY web/.npmrc web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# vite writes to ../internal/webui/dist, i.e. /src/internal/webui/dist.
RUN npm run build

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG VERSION
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN MODULE="$(go list -m)" \
 && GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build -trimpath \
      -ldflags "-s -w -X ${MODULE}/internal/buildinfo.Version=${VERSION}" \
      -o /out/bubbles ./cmd/bubbles \
 && mkdir -p /out/data \
 && chown 65532:65532 /out/data

FROM ${FINAL_IMAGE}
LABEL org.opencontainers.image.title="bubbles" \
      org.opencontainers.image.description="Secure LAN chat in one binary: channels, DMs, files and alerts over HTTPS with a built-in CA." \
      org.opencontainers.image.source="https://github.com/donaldsteele/bubbles" \
      org.opencontainers.image.url="https://donaldsteele.github.io/bubbles/" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/bubbles /bubbles
COPY --from=build --chown=65532:65532 /out/data /data
ENV BUBBLES_DATA_DIR=/data
VOLUME /data
EXPOSE 28225 28226
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/bubbles", "healthcheck"]
USER nonroot
ENTRYPOINT ["/bubbles"]
CMD ["serve"]
