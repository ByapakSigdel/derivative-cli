#!/bin/bash
# install-libs.sh
#
# Installs the external Arduino libraries the visual editor can emit #includes
# for. Unlike the board cores — which are multi-GB and provisioned exactly once
# behind a marker file (see install-cores.sh + docker-entrypoint.sh) — libraries
# are small, so this script runs on EVERY container start and installs only what
# is missing.
#
# Why this matters: the old design installed libraries inside install-cores.sh,
# which the entrypoint runs only on the FIRST boot (gated by the
# .derivative-provisioned marker). Any library added afterwards was therefore
# never installed on an already-provisioned volume — which is exactly how
# `LiquidCrystal_I2C.h: No such file or directory` slipped through for the LCD
# blocks. Running the (cheap, incremental) library step on every start makes
# adding a library take effect on the next restart, with no core re-download and
# no need to recreate the data volume.
#
# This list MUST stay in sync with requiredCliLibraries() in
# Derivative/lib/blocks/libraryRegistry.ts. If you add a block that needs a new
# arduino-cli library, add its registry name here too.
#
# Built-in (ship with a core, NOT listed here): Servo, Wire, SoftwareSerial,
# Stepper, LiquidCrystal.
#
# Environment:
#   ARDUINO_CLI_PATH  — Path to arduino-cli binary (default: arduino-cli)

# NOT `set -e`: installs are best-effort so one unavailable library can't stop
# the server from starting (the matching block just won't compile until fixed).
set -uo pipefail

CLI="${ARDUINO_CLI_PATH:-arduino-cli}"

# arduino-cli registry names of every library a generated sketch may #include.
LIBS=(
  "DHT sensor library"
  "Adafruit Unified Sensor"
  "LiquidCrystal I2C"
  "Adafruit GFX Library"
  "Adafruit SSD1306"
  "TinyGPSPlus"
  "MPU6050"
  "RTClib"
  "Adafruit NeoPixel"
)

echo "=== Arduino Library Check ==="

# `lib list` reports only installed libraries and needs no network. We match
# each desired name against it (case-insensitive, fixed-string) to find what is
# missing. A false "missing" just triggers a redundant (harmless) install; we
# never skip a genuinely-absent library.
installed="$("$CLI" lib list 2>/dev/null || true)"
missing=()
for lib in "${LIBS[@]}"; do
  if ! printf '%s\n' "$installed" | grep -qiF "$lib"; then
    missing+=("$lib")
  fi
done

if [ "${#missing[@]}" -eq 0 ]; then
  echo "[libs] all ${#LIBS[@]} libraries already present — nothing to install"
  exit 0
fi

echo "[libs] ${#missing[@]} missing: ${missing[*]}"
echo "[libs] updating library index ..."
"$CLI" lib update-index || echo "[libs][warn] index update failed; attempting installs anyway"

rc=0
for lib in "${missing[@]}"; do
  echo "[libs] installing: $lib"
  if ! "$CLI" lib install "$lib"; then
    echo "[libs][warn] failed to install: $lib"
    rc=1
  fi
done

echo ""
echo "=== Installed Libraries ==="
"$CLI" lib list || true

exit "$rc"
