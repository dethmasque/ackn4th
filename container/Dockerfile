# ackn4th in a Linux container, with agents' commands sandboxed by
# bubblewrap.
# Use container/ackn4th-container (or `just container`) to run it; it
# passes the flags bubblewrap and the in-memory secret store need. The
# build context is the repository root (.dockerignore is there):
#   docker build -f container/Dockerfile .

# Every dependency is pinned: images by digest, Debian packages to a
# snapshot of the archive, tools and npm packages to exact versions. Bump
# them together, deliberately, and rebuild. The ant and npm versions are
# repeated in the install hints in internal/auth/oauth.go.

FROM golang:1.27.3-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/dethmasque/ackn4th/internal/cli.Version=${VERSION}" -o /out/ackn4th ./cmd/ackn4th
# The Anthropic CLI, which `ackn4th login` uses for Claude's OAuth sign-in.
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/anthropics/anthropic-cli/v2/cmd/ant@v2.4.0

# Node 22, which Claude Code needs, from the official image.
FROM node:22.23.4-bookworm-slim@sha256:4b150b48b4116e2a22f2c39cc27025fbf4f153e2078fd3b14d159fdb5c7279da
# Debian as it was at DEBIAN_SNAPSHOT (over http, as the image's default
# mirror is; apt checks signatures), so the pinned versions (and their
# dependencies) stay installable after newer ones replace them.
ARG DEBIAN_SNAPSHOT=20261101T000000Z
RUN sed -i -e "s|http://deb.debian.org/debian-security|http://snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT}|" \
           -e "s|http://deb.debian.org/debian|http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}|" \
      /etc/apt/sources.list.d/debian.sources \
 && apt-get -o Acquire::Check-Valid-Until=false update \
 && apt-get install -y --no-install-recommends \
      bubblewrap=0.8.0-2+deb12u1 ca-certificates=20250419~deb12u2 curl=7.88.1-10+deb12u15 \
      git=1:2.39.5-0+deb12u3 socat=1.7.4.4-2 tini=0.19.0-1+b3 \
 && rm -rf /var/lib/apt/lists/*
# Agent CLIs to bake in, as npm packages, so your Claude, ChatGPT and
# Copilot plans work without API keys. Pick fewer, or set it empty for a
# smaller image:
#   --build-arg AGENT_CLIS="@anthropic-ai/claude-code@2.1.316"
# (The Cursor CLI has no npm package: install it inside, see the README.)
ARG AGENT_CLIS="@anthropic-ai/claude-code@2.1.316 @openai/codex@0.166.0 @github/copilot@1.0.105"
RUN if [ -n "$AGENT_CLIS" ]; then \
      npm install -g --omit=dev $AGENT_CLIS && npm cache clean --force; \
    fi
# Match the host user so files agents write in the mounted project are yours.
ARG UID=1000
ARG GID=1000
# (The base image's own user would take UID 1000.)
RUN userdel -r node 2>/dev/null; groupadd -g "$GID" ackn4th 2>/dev/null || true \
 && useradd -m -u "$UID" -g "$GID" -s /bin/bash ackn4th \
 && mkdir -p /work /run/ackn4th && chown "$UID:$GID" /work /run/ackn4th && chmod 700 /run/ackn4th
COPY --from=build /out/ackn4th /out/ant /usr/local/bin/
USER ackn4th
# Secrets (the login session, API keys) go to a tmpfs instead of a
# keychain: in memory only, gone when the container stops.
ENV ACKN4TH_SECRETS_DIR=/run/ackn4th \
    ACKN4TH_CONTAINER=1 \
    COLORTERM=truecolor \
    PATH=/home/ackn4th/.local/bin:$PATH
WORKDIR /work
ENTRYPOINT ["/usr/bin/tini", "--"]
# Browser sign-ins listen on the container's localhost; the wrapper
# publishes port 54546, and this forwards it to the sign-in's callback port.
CMD ["sh", "-c", "if [ -n \"$ACKN4TH_OAUTH_PORT\" ]; then socat TCP-LISTEN:54546,fork,reuseaddr TCP:127.0.0.1:$ACKN4TH_OAUTH_PORT & fi; exec sleep infinity"]
