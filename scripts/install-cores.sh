#!/bin/bash
# install-cores.sh
#
# This script is executed during the Docker image build to pre-install
# Arduino board cores. By baking the cores into the image, we avoid
# downloading them at runtime, which would be slow and require internet
# access from the container.
#
# The cores installed here must match the supported boards defined in
# internal/compiler/compiler.go.
#
# Usage:
#   ./scripts/install-cores.sh
#
# Environment:
#   ARDUINO_CLI_PATH — Path to arduino-cli binary (default: arduino-cli)

set -euo pipefail

CLI="${ARDUINO_CLI_PATH:-arduino-cli}"

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

echo ""
echo "=== Core installation complete ==="
