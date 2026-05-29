#!/bin/bash
# install-cores.sh
#
# Provisions Arduino board cores + external libraries into the arduino-cli
# data directory. This runs at RUNTIME (via docker-entrypoint.sh) the first
# time the container starts, writing into a persistent Docker volume — NOT
# baked into the image.
#
# Why runtime instead of build time: the three cores (AVR + ESP32 + ESP8266)
# total several GB once extracted. Baking them into image layers made the
# image huge and the build/unpack ran out of disk on small hosts ("no space
# left on device" while extracting the ESP32 RISC-V toolchain). Living on a
# volume keeps the image tiny, downloads the cores exactly once, and they
# survive image rebuilds.
#
# The cores installed here must match the supported boards defined in
# internal/compiler/compiler.go.
#
# Usage:
#   ./scripts/install-cores.sh
#
# Environment:
#   ARDUINO_CLI_PATH  — Path to arduino-cli binary (default: arduino-cli)
#   ARDUINO_DATA_DIR  — arduino-cli data dir for pruning (default: ~/.arduino15)

set -euo pipefail

CLI="${ARDUINO_CLI_PATH:-arduino-cli}"
DATA_DIR="${ARDUINO_DATA_DIR:-$HOME/.arduino15}"

echo "=== Arduino Core Installation ==="
echo "Using arduino-cli at: $(which "$CLI")"
echo ""

# Update the board index to get the latest core definitions.
echo "--- Updating board index ---"
"$CLI" core update-index

# Install additional board manager URLs for ESP32 and ESP8266.
# These are third-party cores that require extra index URLs.
echo ""
echo "--- Configuring additional board manager URLs ---"
"$CLI" config init --overwrite 2>/dev/null || true
"$CLI" config set board_manager.additional_urls \
    "https://raw.githubusercontent.com/espressif/arduino-esp32/gh-pages/package_esp32_index.json" \
    "https://arduino.esp8266.com/stable/package_esp8266com_index.json"

# Re-update the index to include the new URLs.
echo ""
echo "--- Updating board index with additional URLs ---"
"$CLI" core update-index

# Install Arduino AVR core (Uno, Mega, Nano, Leonardo, etc.)
echo ""
echo "--- Installing Arduino AVR core ---"
"$CLI" core install arduino:avr
echo "Arduino AVR core installed successfully."

# Install ESP32 core.
echo ""
echo "--- Installing ESP32 core ---"
"$CLI" core install esp32:esp32
echo "ESP32 core installed successfully."

# Install ESP8266 core.
echo ""
echo "--- Installing ESP8266 core ---"
"$CLI" core install esp8266:esp8266
echo "ESP8266 core installed successfully."

# Verify installation.
echo ""
echo "=== Installed Cores ==="
"$CLI" core list

# ---------------------------------------------------------------------------
# External Arduino libraries are NOT installed here anymore.
#
# This script is marker-gated by docker-entrypoint.sh — it runs only on the
# first boot. Libraries, however, get added over time as new blocks ship, and
# they need to install on an already-provisioned volume without re-downloading
# the multi-GB cores. So library provisioning lives in install-libs.sh, which
# the entrypoint runs on EVERY start and installs only what is missing.
# (Keeping the library list there also keeps it next to the sync note for
# requiredCliLibraries() in Derivative/lib/blocks/libraryRegistry.ts.)
# ---------------------------------------------------------------------------
# Prune the data dir to keep the volume small. Removes things never needed
# for compilation (debuggers, on-chip-flash tools, docs/examples, oversized
# static libs) and the downloaded archives + caches (the single biggest
# reclaim — these are only needed during install).
# ---------------------------------------------------------------------------
echo ""
echo "--- Pruning unused toolchain pieces ---"
if [ -d "$DATA_DIR/packages" ]; then
  find "$DATA_DIR/packages" -type f -name "*gdb*" -delete 2>/dev/null || true
  find "$DATA_DIR/packages" -type d -name "*gdb*" -exec rm -rf {} + 2>/dev/null || true
  find "$DATA_DIR/packages" -type d -name "*openocd*" -exec rm -rf {} + 2>/dev/null || true
  find "$DATA_DIR/packages" -type d -name "*dfu*" -exec rm -rf {} + 2>/dev/null || true
  for d in doc docs examples tests test share; do
    find "$DATA_DIR/packages" -type d -name "$d" -exec rm -rf {} + 2>/dev/null || true
  done
  find "$DATA_DIR/packages" -name "*.a" -path "*/lib/lib*.a" -size +5M -delete 2>/dev/null || true
fi

echo "--- Clearing download cache + staging archives ---"
"$CLI" cache clean 2>/dev/null || true
rm -rf "$DATA_DIR/staging" "$DATA_DIR/tmp" 2>/dev/null || true

echo ""
echo "=== Provisioning complete ==="
