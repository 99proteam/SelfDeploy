FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/selfdeploy ./cmd/selfdeploy

FROM alpine:3.20
RUN apk add --no-cache docker-cli docker-cli-compose docker-cli-buildx git openssh-client ca-certificates tzdata curl
# Nixpacks builds apps without a Dockerfile (Node, Python, Go, PHP, Ruby, Rust, Java ...).
ARG TARGETARCH
ARG NIXPACKS_VERSION=1.39.0
RUN arch=$([ "$TARGETARCH" = "arm64" ] && echo aarch64 || echo x86_64) && \
    curl -fsSL "https://github.com/railwayapp/nixpacks/releases/download/v${NIXPACKS_VERSION}/nixpacks-v${NIXPACKS_VERSION}-${arch}-unknown-linux-musl.tar.gz" \
      | tar -xz -C /usr/local/bin nixpacks \
    || echo "warning: nixpacks not installed; apps need a Dockerfile"
COPY --from=build /out/selfdeploy /usr/local/bin/selfdeploy
ENV SELFDEPLOY_DATA=/data/selfdeploy SELFDEPLOY_ADDR=:8000
EXPOSE 8000
VOLUME /data/selfdeploy
HEALTHCHECK --interval=30s --timeout=5s CMD curl -fsS http://127.0.0.1:8000/healthz || exit 1
ENTRYPOINT ["selfdeploy"]
