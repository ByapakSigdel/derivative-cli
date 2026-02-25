# =============================================================================
# Arduino Compiler API — Multi-Stage Dockerfile (Optimized for low-resource VPS)
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
# Optimizations applied to reduce image size from ~1.2GB to ~400-500MB:
# - Removed debug tools (GDB, openocd) not needed for compilation
# - Removed unused docs, examples, and test files from cores
# - Moved arduino data via mv instead of cp to avoid duplication during build
# - Combined RUN layers to reduce intermediate layer sizes
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

# Create non-root user early so we can install cores directly into their home.
RUN useradd --create-home --shell /bin/bash appuser

# Install runtime dependencies, arduino-cli, cores, and clean up — all in one
# layer to minimize image size and avoid large intermediate layers.
# python3 is required by ESP32/ESP8266 build tools.
# git is required by some library managers.
COPY scripts/install-cores.sh /tmp/install-cores.sh
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        curl \
        ca-certificates \
        python3 \
        python3-serial \
        git \
    && rm -rf /var/lib/apt/lists/* \
    # Install arduino-cli
    && curl -fsSL https://raw.githubusercontent.com/arduino/arduino-cli/master/install.sh | \
       BINDIR=/usr/local/bin sh \
    && arduino-cli version \
    # Install board cores
    && chmod +x /tmp/install-cores.sh && /tmp/install-cores.sh && rm /tmp/install-cores.sh \
    # -----------------------------------------------------------------------
    # Remove debug tools (GDB, openocd, etc.) — not needed for compilation.
    # These are the biggest space offenders (~200-300MB).
    # The error "no space left on device" specifically pointed to
    # xtensa-esp-elf-gdb which is ~150MB alone.
    # -----------------------------------------------------------------------
    && find /root/.arduino15/packages -type f -name "*gdb*" -delete \
    && find /root/.arduino15/packages -type d -name "*gdb*" -exec rm -rf {} + 2>/dev/null || true \
    && find /root/.arduino15/packages -type d -name "*openocd*" -exec rm -rf {} + 2>/dev/null || true \
    # Remove DFU tools (device firmware upgrade via USB — not needed for compilation)
    && find /root/.arduino15/packages -type d -name "*dfu*" -exec rm -rf {} + 2>/dev/null || true \
    # Remove documentation, examples, and test files from cores
    && find /root/.arduino15/packages -type d -name "doc" -exec rm -rf {} + 2>/dev/null || true \
    && find /root/.arduino15/packages -type d -name "docs" -exec rm -rf {} + 2>/dev/null || true \
    && find /root/.arduino15/packages -type d -name "examples" -exec rm -rf {} + 2>/dev/null || true \
    && find /root/.arduino15/packages -type d -name "tests" -exec rm -rf {} + 2>/dev/null || true \
    && find /root/.arduino15/packages -type d -name "test" -exec rm -rf {} + 2>/dev/null || true \
    && find /root/.arduino15/packages -type d -name "share" -exec rm -rf {} + 2>/dev/null || true \
    # Remove debug symbols and static libraries we don't need
    && find /root/.arduino15/packages -name "*.a" -path "*/lib/lib*.a" -size +5M -delete 2>/dev/null || true \
    # Move (not copy) arduino data to appuser home to avoid duplication
    && mv /root/.arduino15 /home/appuser/.arduino15 \
    && chown -R appuser:appuser /home/appuser/.arduino15

# Copy the compiled Go binary from the builder stage.
COPY --from=builder /arduino-compiler /usr/local/bin/arduino-compiler

# Create the temp directory for compilations.
RUN mkdir -p /tmp/arduino-compile && chown appuser:appuser /tmp/arduino-compile

# Switch to non-root user.
USER appuser
WORKDIR /home/appuser

EXPOSE 8080

# Health check using the Go binary's built-in wget-style check avoids needing curl at runtime,
# but since curl is already installed, we keep it simple.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

ENTRYPOINT ["arduino-compiler"]
