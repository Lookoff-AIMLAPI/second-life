# syntax=docker/dockerfile:1
FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=0.0.0-dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/Lookoff-AIMLAPI/second-life/internal/bridge.Version=${VERSION}" -o /out/slife ./cmd/slife

FROM node:24-slim
# slife drives the codex CLI for the login, token refresh and limits.
RUN npm install -g --no-audit --no-fund @openai/codex@0.146.0 \
  && npm cache clean --force \
  && install -d -o node -g node -m 0700 /data /data/codex /data/slife
# The slim image has no CA store.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/slife /usr/local/bin/slife

ENV CODEX_HOME=/data/codex \
    SLIFE_HOME=/data/slife \
    SLIFE_ADDR=0.0.0.0:8787
USER node
WORKDIR /data
VOLUME /data
EXPOSE 8787
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["node", "-e", "fetch('http://127.0.0.1:8787/healthz').then(r => process.exit(r.ok ? 0 : 1), () => process.exit(1))"]
ENTRYPOINT ["slife"]
CMD ["serve"]
