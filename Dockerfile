# syntax=docker/dockerfile:1

# --- Stage 1: build the SPA -------------------------------------------------
# Both build stages run on the build machine's own platform and cross-compile
# for the target (TARGETOS/TARGETARCH), so a multi-arch build does not run npm
# and the Go compiler under emulation.
FROM --platform=$BUILDPLATFORM node:26-alpine AS frontend
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
# Resilient install on flaky networks: more retries, longer timeouts, a single
# socket and no audit/fund chatter. (npm 10 surfaces registry/network failures
# as the cryptic "Exit handler never called!".)
# Strictly `npm ci`: it installs exactly the committed lockfile and fails when
# package.json and package-lock.json disagree. There is deliberately no
# `|| npm install` fallback -- that silently re-resolved the tree on any
# failure, so lockfile drift (or a transient error) shipped an unreviewed,
# unscanned dependency set instead of breaking the build.
RUN npm config set fetch-retries 6 && \
    npm config set fetch-retry-mintimeout 20000 && \
    npm config set fetch-retry-maxtimeout 180000 && \
    npm config set fetch-timeout 600000 && \
    npm config set maxsockets 1 && \
    npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# --- Stage 2: build the Go binary (embeds the SPA) --------------------------
# Pinned to an exact patch, not the floating 1.27 tag: go.mod requires >= 1.27.2
# (that floor is what actually fixes the standard-library CVEs in the shipped
# binary), the floating tag was still on 1.26.5, and the official golang images
# set GOTOOLCHAIN=local -- so Go does NOT auto-download the newer toolchain to
# rescue the build, it just fails. Bump this together with go.mod.
FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine AS backend
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /app/backend
COPY backend/go.mod backend/go.sum* ./
COPY backend/ ./
# `go build` fetches exactly the modules the build needs (verified against
# go.sum); a blanket `go mod download` would also pull dependencies' test-only
# modules, which we don't need.
# Replace the placeholder SPA with the freshly built assets, then compile.
RUN rm -rf internal/web/dist
COPY --from=frontend /app/frontend/dist ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w -X main.version=${VERSION}" -o /uploader ./cmd/uploader

# --- Stage 3: runtime -------------------------------------------------------
FROM alpine:3.24
# The runtime image still installs no extra packages -- the binary drops
# privileges itself (no su-exec) and image processing is pure Go; `adduser` and
# `mkdir` are busybox built-ins.
#
# It does run `apk upgrade`, which the previous version deliberately avoided so
# the build needed no network to the Alpine repos. That property is gone on
# purpose: alpine:3.24.1 ships openssl 3.5.7-r0 while the v3.24 repository
# already has 3.5.8-r0 (CVE-2026-14456, HIGH in libcrypto3/libssl3), and the
# image scan in .gitea/workflows/security.yml blocks the push over exactly that.
# Shipping a CVE that upstream has already fixed is the worse trade, and the
# build reaches the network for npm, Go modules and the base images anyway.
RUN apk --no-cache upgrade && adduser -D -u 10001 uploader && mkdir -p /data/files
COPY --from=backend /uploader /usr/local/bin/uploader
# Static ffmpeg/ffprobe from Docker Hub (no apk needed) — enables video
# thumbnails and HLS transcoding. Remove these two lines to ship a smaller,
# ffmpeg-less image (video thumbnails/HLS simply won't be generated).
# Pinned to an exact patch release: ffmpeg parses untrusted user uploads, so the
# image scan in .gitea/workflows/security.yml should be the thing that tells us
# to move this forward — not a floating tag that changes silently.
COPY --from=mwader/static-ffmpeg:8.1.2 /ffmpeg /ffprobe /usr/local/bin/
EXPOSE 8080
# Starts as root so it can fix ownership of a bind-mounted /data, then the
# binary drops to the unprivileged uploader user (uid 10001) before serving.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["uploader", "healthcheck"]
ENTRYPOINT ["uploader"]
CMD ["serve"]
