# =============================================================================
# Arduino Compiler API — Multi-Stage Dockerfile (Optimized for low-resource VPS)
# =============================================================================
#
# Stage 1 (builder): Compiles the Go binary using the official Go image.
# Stage 2 (runtime): Debian + arduino-cli + the Go binary. NOTHING heavy is
#                     baked in — board cores and libraries are NOT installed
#                     at build time.
#
# Why: the AVR + ESP32 + ESP8266 cores total several GB extracted. Baking
# them into image layers made the image enormous and the build/unpack ran
# out of disk on small hosts ("no space left on device" while extracting the
# ESP32 RISC-V toolchain). Instead, cores + libraries are provisioned at
# RUNTIME into a persistent Docker volume on first start (see
# docker-entrypoint.sh + install-cores.sh). The image stays tiny, the heavy
# downloads happen once and live on disk, and they survive image rebuilds.
#
# Build:
#   docker build -t arduino-compiler .
#
# Run (mount a volume so cores persist — see docker-compose.yml):
#   docker run -p 8080:8080 -v arduino_data:/home/appuser/.arduino15 arduino-compiler
# =============================================================================

# ---------------------------------------------------------------------------
# Stage 1: Build the Go binary
# ---------------------------------------------------------------------------
FROM golang:1.24-bookworm AS builder

WORKDIR /app

# Copy dependency files first to leverage Docker layer caching.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code.
COPY . .

# Build a statically-linked binary for the target platform.
RUN CGO_ENABLED=0 go build \
    -ldflags="-w -s" \
    -o /arduino-compiler \
    .

# ---------------------------------------------------------------------------
# Stage 2: Runtime image with arduino-cli and board cores
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim

# Create non-root user. Cores get provisioned into this user's home at
# runtime (onto a mounted volume), so nothing heavy lives in the image.
RUN useradd --create-home --shell /bin/bash appuser

# Install runtime deps + arduino-cli only. No cores, no libraries — those
# are downloaded at first start by docker-entrypoint.sh.
# python3 is required by ESP32/ESP8266 build tools; git by some lib managers.
COPY scripts/install-cores.sh /usr/local/bin/install-cores.sh
COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        curl \
        ca-certificates \
        python3 \
        python3-serial \
        git \
    && rm -rf /var/lib/apt/lists/* \
    # Install arduino-cli.
    && curl -fsSL https://raw.githubusercontent.com/arduino/arduino-cli/master/install.sh | \
       BINDIR=/usr/local/bin sh \
    && arduino-cli version \
    && chmod +x /usr/local/bin/install-cores.sh /usr/local/bin/docker-entrypoint.sh \
    # Pre-create the data dir owned by appuser. When a fresh named volume is
    # mounted here, Docker seeds it with this dir's ownership, so the runtime
    # provisioning (running as appuser) can write into the volume.
    && mkdir -p /home/appuser/.arduino15 \
    && chown -R appuser:appuser /home/appuser/.arduino15

# Copy the compiled Go binary from the builder stage.
COPY --from=builder /arduino-compiler /usr/local/bin/arduino-compiler

# Create the temp directory for compilations.
RUN mkdir -p /tmp/arduino-compile && chown appuser:appuser /tmp/arduino-compile

# Switch to non-root user.
USER appuser
WORKDIR /home/appuser

EXPOSE 8080

# Generous start period: the first boot provisions cores (a multi-GB,
# multi-minute download) before the server starts serving. Subsequent boots
# find the volume already provisioned and start in seconds.
HEALTHCHECK --interval=30s --timeout=5s --start-period=900s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

ENTRYPOINT ["docker-entrypoint.sh"]
