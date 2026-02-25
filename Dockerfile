# =============================================================================
# Arduino Compiler API — Multi-Stage Dockerfile
# =============================================================================
#
# This Dockerfile builds the Arduino compiler API server in two stages:
#
# Stage 1 (builder): Compiles the Go binary using the official Go image.
# Stage 2 (runtime): Sets up a Debian-based image with arduino-cli and the
#                     pre-compiled Go binary. Board cores are installed at
#                     build time so they're baked into the image.
#
# Build:
#   docker build -t arduino-compiler .
#
# Run:
#   docker run -p 8080:8080 arduino-compiler
#
# The final image is ~800MB–1.2GB primarily due to the ESP32 core (~500MB).
# =============================================================================

# ---------------------------------------------------------------------------
# Stage 1: Build the Go binary
# ---------------------------------------------------------------------------
FROM golang:1.24-bookworm AS builder

WORKDIR /app

# Copy dependency files first to leverage Docker layer caching.
# These layers are invalidated only when dependencies change.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code.
COPY . .

# Build a statically-linked binary for the target platform.
# CGO_ENABLED=0 ensures no C library dependencies in the final binary.
RUN CGO_ENABLED=0 go build \
    -ldflags="-w -s" \
    -o /arduino-compiler \
    .

# ---------------------------------------------------------------------------
# Stage 2: Runtime image with arduino-cli and board cores
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim

# Install runtime dependencies:
# - curl: needed to download arduino-cli
# - ca-certificates: needed for HTTPS connections (board index downloads)
# - python3: required by some board cores (ESP32, ESP8266) for build tools
# - git: required by some library managers
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        curl \
        ca-certificates \
        python3 \
        python3-serial \
        git \
    && rm -rf /var/lib/apt/lists/*

# Install arduino-cli.
# We use the official install script which downloads the latest stable release.
RUN curl -fsSL https://raw.githubusercontent.com/arduino/arduino-cli/master/install.sh | \
    BINDIR=/usr/local/bin sh

# Verify arduino-cli installation.
RUN arduino-cli version

# Copy the core installation script and run it.
# This installs Arduino AVR, ESP32, and ESP8266 board cores into the image.
# Running this at build time means cores are pre-installed and available
# immediately at runtime without any network access.
COPY scripts/install-cores.sh /tmp/install-cores.sh
RUN chmod +x /tmp/install-cores.sh && /tmp/install-cores.sh && rm /tmp/install-cores.sh

# Copy the compiled Go binary from the builder stage.
COPY --from=builder /arduino-compiler /usr/local/bin/arduino-compiler

# Create a non-root user for security.
# The server does not need root privileges to operate.
RUN useradd --create-home --shell /bin/bash appuser

# Create the temp directory for compilations and give ownership to appuser.
RUN mkdir -p /tmp/arduino-compile && chown appuser:appuser /tmp/arduino-compile

# Copy arduino-cli data to appuser's home so it can access cores.
# arduino-cli stores data in ~/.arduino15 by default.
RUN cp -r /root/.arduino15 /home/appuser/.arduino15 && \
    chown -R appuser:appuser /home/appuser/.arduino15

# Switch to non-root user.
USER appuser

# Set the working directory.
WORKDIR /home/appuser

# Expose the default server port.
EXPOSE 8080

# Health check: verify the server is responding.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

# Run the server.
ENTRYPOINT ["arduino-compiler"]
