# syntax=docker/dockerfile:1

FROM node:24-trixie-slim AS frontend
WORKDIR /src/web
RUN npm install --global pnpm@12.3.4
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.25-trixie AS backend
RUN apt-get update && apt-get install -y --no-install-recommends libvips-dev pkg-config && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=frontend /src/web/dist ./web/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -tags vips -ldflags='-s -w' -o /archika ./cmd/archika

# Diagnostic image for fixture generation; build explicitly with --target verification.
FROM backend AS verification
RUN apt-get update && apt-get install -y --no-install-recommends strace time 7zip libvips-tools libheif-plugin-aomenc libheif-plugin-dav1d && rm -rf /var/lib/apt/lists/*

# Runtime image: only the binary and shared libraries, no toolchains, headers or node_modules.
FROM debian:trixie-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libvips42t64 libheif-plugin-dav1d && rm -rf /var/lib/apt/lists/*
COPY --from=backend /archika /usr/local/bin/archika
COPY LICENSE NOTICE THIRD_PARTY_NOTICES.txt /usr/share/doc/archika/
USER 1000:1000
WORKDIR /app
ENV ARCHIKA_CONFIG=/config/archika.json
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/archika"]
CMD ["serve"]
