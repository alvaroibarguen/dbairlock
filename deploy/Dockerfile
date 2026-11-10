# syntax=docker/dockerfile:1
#
# dbairlock container image. Build from the repository root:
#
#   docker build -f deploy/Dockerfile -t dbairlock:dev .
#   make image
#
# Multi-arch (linux/amd64 and linux/arm64) with buildx:
#
#   docker buildx build -f deploy/Dockerfile --platform linux/amd64,linux/arm64 .
#
# The web and build stages always run on the build machine's architecture.
# The Go binary needs cgo (pg_query_go links C code), so a build for another
# architecture cross-compiles with Debian's cross gcc instead of emulating the
# whole toolchain. Base images are pinned by tag and digest; the digests are
# recorded in docs/VERSIONS.md.

ARG GO_IMAGE=golang:1.27.3-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80
ARG NODE_IMAGE=node:26-trixie-slim@sha256:5adad64f9a1fca2d51972caa65983634c8a774eb300d72b20f7f2a0f60368072
ARG RUNTIME_IMAGE=gcr.io/distroless/base-nossl-debian13:nonroot@sha256:e6b0f82595aa1edde72c8054de7a9dca30f0f8f54820c37a167c0ddfc21c5c67

# ---------------------------------------------------------------------------
# web: build the dashboard with Vite. The output is architecture independent.
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS web
# Node 26 no longer ships corepack, so install the pinned pnpm with npm.
ARG PNPM_VERSION=12.18.0
RUN npm install -g pnpm@${PNPM_VERSION} && pnpm --version
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml web/.pnpmfile.cjs ./
RUN --mount=type=cache,id=dbairlock-pnpm-store,target=/pnpm-store \
    pnpm install --frozen-lockfile --store-dir /pnpm-store
COPY web/ ./
RUN pnpm build

# ---------------------------------------------------------------------------
# build: compile the Go binary with cgo and the dashboard embedded.
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ARG BUILDARCH
# Cross gcc and the target's libc headers, only when cross-compiling.
RUN set -eu; \
    if [ "${TARGETARCH}" != "${BUILDARCH}" ]; then \
      case "${TARGETARCH}" in \
        amd64) pkgs="gcc-x86-64-linux-gnu libc6-dev-amd64-cross" ;; \
        arm64) pkgs="gcc-aarch64-linux-gnu libc6-dev-arm64-cross" ;; \
        *) echo "unsupported TARGETARCH ${TARGETARCH}" >&2; exit 1 ;; \
      esac; \
      apt-get update; \
      apt-get install -y --no-install-recommends ${pkgs}; \
      rm -rf /var/lib/apt/lists/*; \
    fi
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=dbairlock-gomod,target=/go/pkg/mod \
    go mod download
COPY . .
COPY --from=web /src/web/dist/ ./internal/webui/dist/
# The build context has no .git directory, so version, commit and date come
# in as build arguments (make image passes them) and are linked into the
# binary; they also become the OCI labels below.
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=
RUN --mount=type=cache,id=dbairlock-gomod,target=/go/pkg/mod \
    --mount=type=cache,id=dbairlock-gobuild-${TARGETARCH},target=/root/.cache/go-build \
    set -eu; \
    if [ "${TARGETARCH}" != "${BUILDARCH}" ]; then \
      case "${TARGETARCH}" in \
        amd64) export CC=x86_64-linux-gnu-gcc ;; \
        arm64) export CC=aarch64-linux-gnu-gcc ;; \
      esac; \
    fi; \
    CGO_ENABLED=1 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
      go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
        -o /out/dbairlock ./cmd/dbairlock; \
    mkdir -p /out/data

# ---------------------------------------------------------------------------
# runtime: distroless glibc base, no shell, non-root.
# ---------------------------------------------------------------------------
FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
LABEL org.opencontainers.image.title="dbairlock" \
      org.opencontainers.image.description="Human review for row-level SQL changes from engineers and AI agents" \
      org.opencontainers.image.source="https://github.com/alvaroibarguen/dbairlock" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}"

COPY --from=build /out/dbairlock /usr/local/bin/dbairlock
# A fresh named volume copies this directory's owner, so the non-root user
# can write the database and master.key.
COPY --from=build --chown=65532:65532 /out/data /data

# 65532 is distroless's "nonroot" user.
USER 65532:65532
VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --start-interval=2s --retries=3 \
    CMD ["/usr/local/bin/dbairlock", "healthcheck", "--listen", ":8080"]

# Other subcommands run with the same entrypoint, for example:
#   docker run --rm -v dbairlock-data:/data dbairlock recover --data-dir /data
ENTRYPOINT ["/usr/local/bin/dbairlock"]
CMD ["serve", "--data-dir", "/data", "--listen", ":8080"]
