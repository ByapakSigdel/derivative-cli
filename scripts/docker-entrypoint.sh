#!/bin/bash
# docker-entrypoint.sh
#
# Provisions Arduino cores + libraries into the data volume on first start,
# then hands off to the compiler server. Cores live on a mounted volume
# (not in the image), so the image stays small and the multi-GB toolchains
# are downloaded once and persist across restarts/rebuilds.
#
# A marker file in the data dir records a successful provision so we only
# pay the download cost once. A failed provision (e.g. transient network)
# leaves no marker, so the next restart retries; the server still starts in
# the meantime (compiles just fail with a clear message until cores exist).

set -uo pipefail

DATA_DIR="${ARDUINO_DATA_DIR:-$HOME/.arduino15}"
MARKER="${DATA_DIR}/.derivative-provisioned"

if [ ! -f "$MARKER" ]; then
  echo "[entrypoint] No provisioning marker found — installing Arduino cores."
  echo "[entrypoint] This is a one-time download (several minutes) into the data volume;"
  echo "[entrypoint] it persists across restarts and image rebuilds."
  mkdir -p "$DATA_DIR"
  if /usr/local/bin/install-cores.sh; then
    touch "$MARKER"
    echo "[entrypoint] Core provisioning complete."
  else
    echo "[entrypoint] WARNING: core provisioning did not finish cleanly. The server will"
    echo "[entrypoint] start anyway; compiles may fail until cores are present. It will"
    echo "[entrypoint] retry on the next restart."
  fi
else
  echo "[entrypoint] Cores already provisioned in the data volume — skipping core install."
fi

# Libraries are checked on EVERY start (NOT marker-gated): they are small and
# the set grows as new blocks ship, so a library added after the first boot
# must still install on an already-provisioned volume. install-libs.sh is
# incremental — it installs only what's missing and is a fast no-op once all
# libraries are present. This is what self-heals a stale volume that predates a
# newly-added library (e.g. LiquidCrystal_I2C for the LCD blocks).
echo "[entrypoint] Ensuring Arduino libraries are present ..."
/usr/local/bin/install-libs.sh || \
  echo "[entrypoint] WARNING: some libraries failed to install; matching blocks may not compile."

# Hand off to the compiler server (PID 1, receives signals).
exec /usr/local/bin/arduino-compiler "$@"
