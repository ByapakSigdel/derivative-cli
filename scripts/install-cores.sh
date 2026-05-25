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

# ---------------------------------------------------------------------------
# External Arduino libraries.
#
# The visual editor can emit sketches that #include these. arduino-cli's
# `compile` automatically picks up libraries installed here (into the user
# libraries dir), so baking them into the image means library-dependent
# sketches compile with no per-request network installs.
#
# This list MUST stay in sync with requiredCliLibraries() in
# Derivative/lib/blocks/libraryRegistry.ts. If you add a block that needs a
# new library, add its arduino-cli name here too.
#
# Built-in (ship with the core, NOT installed here): Servo, Wire,
# SoftwareSerial.
echo ""
echo "--- Updating library index ---"
"$CLI" lib update-index

echo ""
echo "--- Installing external libraries ---"
# Adafruit DHT (+ its Unified Sensor dependency).
"$CLI" lib install "DHT sensor library"
"$CLI" lib install "Adafruit Unified Sensor"
# 16x2 I2C LCD.
"$CLI" lib install "LiquidCrystal I2C"
# OLED SSD1306 (+ GFX; BusIO is pulled in as a dependency).
"$CLI" lib install "Adafruit GFX Library"
"$CLI" lib install "Adafruit SSD1306"
# GPS (NEO-6M / NEO-8M).
"$CLI" lib install "TinyGPSPlus"
# MPU6050 accelerometer + gyroscope (Electronic Cats).
"$CLI" lib install "MPU6050"

echo ""
echo "=== Installed Libraries ==="
"$CLI" lib list

echo ""
echo "=== Core + library installation complete ==="
