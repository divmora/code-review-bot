# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X github.com/divmora/code-review-bot/internal/version.Version=${VERSION}" \
    -o /out/codereview ./cmd/codereview

# git is needed for `review --local`; ca-certificates for HTTPS to providers.
FROM debian:bookworm-slim
ARG VERSION=0.0.0-dev
LABEL org.opencontainers.image.title="code-review-bot" \
      org.opencontainers.image.description="AI code review for GitHub and GitLab with any LLM provider" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/divmora/code-review-bot" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.vendor="DIVMORA Technologies"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && git config --system --add safe.directory '*' \
    && useradd --system --uid 10001 --home /home/app --create-home app
COPY --from=builder /out/codereview /usr/local/bin/codereview
USER 10001
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/codereview"]
CMD ["--help"]
