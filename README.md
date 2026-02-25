# Derivative Server Side Compiler.

A Dockerized REST API server that compiles Arduino C++ sketches remotely and returns ready-to-flash firmware binaries. Built with Go, powered by [arduino-cli](https://arduino.github.io/arduino-cli/), and packaged in Docker with pre-installed board cores — users don't need to install any Arduino toolchain locally.

Submit raw `.ino` code and a target board, get back a compiled `.hex` (AVR) or `.bin` (ESP32/ESP8266) file that can be uploaded directly to hardware from a browser or any HTTP client.

---

## Table of Contents

- [Features](#features)
- [Quick Start](#quick-start)
- [API Reference](#api-reference)
  - [Health Check](#health-check)
  - [List Boards](#list-boards)
  - [Compile Sketch](#compile-sketch)
- [Supported Boards](#supported-boards)
- [Configuration](#configuration)
- [Architecture](#architecture)
  - [Project Structure](#project-structure)
  - [Compilation Flow](#compilation-flow)
  - [Middleware Stack](#middleware-stack)
  - [Error Handling](#error-handling)
- [Development](#development)
  - [Prerequisites](#prerequisites)
  - [Running Locally (without Docker)](#running-locally-without-docker)
  - [Running Tests](#running-tests)
- [Deployment](#deployment)
  - [Docker Compose (Recommended)](#docker-compose-recommended)
  - [Docker CLI](#docker-cli)
  - [Behind a Reverse Proxy](#behind-a-reverse-proxy)
  - [Resource Requirements](#resource-requirements)
- [Security](#security)
- [Roadmap](#roadmap)
  - [Additional Board Support](#additional-board-support)
  - [Library Management](#library-management)
  - [Compilation Features](#compilation-features)
  - [Platform & Infrastructure](#platform--infrastructure)
  - [Developer Experience](#developer-experience)
- [Contributing](#contributing)
- [License](#license)

---

## Features

- **Zero local setup** — No Arduino IDE, no toolchain, no drivers. Just send HTTP requests.
- **16 supported boards** — Arduino AVR (Uno, Mega, Nano, Leonardo, Micro, Pro, Diecimila), ESP32 (ESP32, S3, C3, Wrover), ESP8266 (Generic, NodeMCU, D1 Mini variants).
- **Binary output** — Returns compiled `.hex` or `.bin` files ready for direct hardware upload.
- **Browser-ready CORS** — Full CORS support with exposed custom headers for frontend integration.
- **Concurrency control** — Semaphore-based limit on parallel compilations to prevent resource exhaustion.
- **Per-IP rate limiting** — Token bucket rate limiter with automatic stale entry cleanup.
- **Configurable timeouts** — Per-compilation timeout with automatic process termination.
- **Non-root Docker** — Runs as an unprivileged user inside the container.
- **Health monitoring** — Health endpoint reports arduino-cli version, installed cores, and uptime.
- **Graceful degradation** — Server starts even without arduino-cli; health endpoint reports `degraded` status.

---

## Quick Start

```bash
# Clone the repository
git clone https://github.com/ByapakSigdel/derivative-cli/
cd derivative-cli

# Build and start (first build takes ~10-15 min for board core downloads)
docker compose up --build

# In another terminal, compile a sketch
curl -X POST http://localhost:8080/api/compile \
  -H "Content-Type: application/json" \
  -d '{"code":"void setup(){ pinMode(13,OUTPUT); } void loop(){ digitalWrite(13,HIGH); delay(1000); digitalWrite(13,LOW); delay(1000); }","board":"arduino:avr:uno"}' \
  --output blink.hex

# Upload blink.hex to your Arduino Uno using avrdude, Arduino IDE, or a browser-based uploader
```

---

## API Reference

### Health Check

Returns server status, arduino-cli version, installed board cores, and uptime.

```
GET /health
```

**Response** `200 OK`

```json
{
  "status": "ok",
  "arduino_cli": "arduino-cli  Version: 1.4.1 Commit: e39419312 Date: 2026-01-19T16:13:12Z",
  "installed_cores": "ID              Installed Latest Name\narduino:avr     1.8.7     1.8.7  Arduino AVR Boards\nesp32:esp32     3.3.7     3.3.7  esp32\nesp8266:esp8266 3.1.2     3.1.2  esp8266",
  "uptime": "2h30m15s"
}
```

The `status` field is `"ok"` when arduino-cli is available, or `"degraded"` when it is not (compilations will fail but the server remains operational).

---

### List Boards

Returns all supported board FQBNs with human-readable names, sorted alphabetically.

```
GET /api/boards
```

**Response** `200 OK`

```json
{
  "boards": [
    { "fqbn": "arduino:avr:uno", "name": "Arduino Uno" },
    { "fqbn": "esp32:esp32:esp32", "name": "ESP32 Dev Module" },
    { "fqbn": "esp8266:esp8266:nodemcuv2", "name": "NodeMCU 1.0 (ESP-12E)" }
  ],
  "count": 16
}
```

---

### Compile Sketch

Compiles Arduino C++ code for a specified board and returns the compiled binary.

```
POST /api/compile
Content-Type: application/json
```

**Request Body**

```json
{
  "code": "void setup() { pinMode(13, OUTPUT); } void loop() { digitalWrite(13, HIGH); delay(500); digitalWrite(13, LOW); delay(500); }",
  "board": "arduino:avr:uno"
}
```

| Field   | Type   | Required | Description |
|---------|--------|----------|-------------|
| `code`  | string | Yes      | Raw Arduino C++ sketch source code |
| `board` | string | Yes      | Fully Qualified Board Name (FQBN) from `/api/boards` |

**Success Response** `200 OK`

Returns the compiled binary as a file download.

| Header | Example Value |
|--------|---------------|
| `Content-Type` | `application/octet-stream` |
| `Content-Disposition` | `attachment; filename="sketch.hex"` |
| `Content-Length` | `1265` |
| `X-Board-Name` | `Arduino Uno` |
| `X-Filename` | `sketch.hex` |

The response body is the raw binary data (Intel HEX for AVR boards, raw binary for ESP boards).

**Error Responses**

All error responses return JSON:

```json
{
  "error": "compilation_error",
  "message": "compilation failed",
  "details": "sketch.ino:1:1: error: expected unqualified-id before 'this'..."
}
```

| HTTP Status | Error Type | Cause |
|-------------|-----------|-------|
| `400` | `validation_error` | Empty code, unsupported board FQBN, malformed JSON |
| `408` | `timeout_error` | Compilation exceeded the configured timeout |
| `422` | `compilation_error` | Code has syntax errors, missing libraries, etc. |
| `429` | `capacity_error` | Server at maximum concurrent compilation limit |
| `500` | `internal_error` | Filesystem errors, arduino-cli not found, etc. |

---

### Example: Browser Fetch (JavaScript)

```javascript
const response = await fetch('http://localhost:8080/api/compile', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({
    code: 'void setup(){} void loop(){}',
    board: 'arduino:avr:uno'
  })
});

if (response.ok) {
  const blob = await response.blob();
  const filename = response.headers.get('X-Filename'); // "sketch.hex"
  const boardName = response.headers.get('X-Board-Name'); // "Arduino Uno"

  // Trigger download
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
}
```

The server sets `Access-Control-Expose-Headers` so browsers can read `Content-Disposition`, `X-Board-Name`, `X-Filename`, and `Content-Length` from JavaScript.

---

### Example: curl

```bash
# Compile for Arduino Uno (AVR — outputs .hex)
curl -X POST http://localhost:8080/api/compile \
  -H "Content-Type: application/json" \
  -d '{"code":"void setup(){} void loop(){}","board":"arduino:avr:uno"}' \
  --output sketch.hex

# Compile for ESP32 (outputs .bin)
curl -X POST http://localhost:8080/api/compile \
  -H "Content-Type: application/json" \
  -d '{"code":"void setup(){} void loop(){}","board":"esp32:esp32:esp32"}' \
  --output sketch.bin

# Compile for NodeMCU ESP8266 (outputs .bin)
curl -X POST http://localhost:8080/api/compile \
  -H "Content-Type: application/json" \
  -d '{"code":"void setup(){} void loop(){}","board":"esp8266:esp8266:nodemcuv2"}' \
  --output sketch.bin
```

---

## Supported Boards

### Arduino AVR

| FQBN | Board Name |
|------|-----------|
| `arduino:avr:uno` | Arduino Uno |
| `arduino:avr:mega` | Arduino Mega 2560 |
| `arduino:avr:nano` | Arduino Nano |
| `arduino:avr:leonardo` | Arduino Leonardo |
| `arduino:avr:micro` | Arduino Micro |
| `arduino:avr:pro` | Arduino Pro / Pro Mini |
| `arduino:avr:diecimila` | Arduino Duemilanove / Diecimila |

Output format: Intel HEX (`.hex`)

### ESP32

| FQBN | Board Name |
|------|-----------|
| `esp32:esp32:esp32` | ESP32 Dev Module |
| `esp32:esp32:esp32s3` | ESP32-S3 Dev Module |
| `esp32:esp32:esp32c3` | ESP32-C3 Dev Module |
| `esp32:esp32:esp32wrover` | ESP32 Wrover Module |

Output format: Binary (`.bin`)

### ESP8266

| FQBN | Board Name |
|------|-----------|
| `esp8266:esp8266:generic` | ESP8266 Generic Module |
| `esp8266:esp8266:nodemcuv2` | NodeMCU 1.0 (ESP-12E) |
| `esp8266:esp8266:d1_mini` | LOLIN (Wemos) D1 Mini |
| `esp8266:esp8266:d1_mini_pro` | LOLIN (Wemos) D1 Mini Pro |
| `esp8266:esp8266:d1_mini_lite` | LOLIN (Wemos) D1 Mini Lite |

Output format: Binary (`.bin`)

---

## Configuration

All settings are controlled via environment variables with sensible defaults. Invalid numeric values are clamped to safe minimums with a warning log.

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | TCP port the server listens on |
| `MAX_CONCURRENT_COMPILATIONS` | `5` | Maximum parallel compilations (min: 1). Additional requests get HTTP 429. |
| `COMPILE_TIMEOUT` | `120` | Per-compilation timeout in seconds (min: 10). Process is killed if exceeded. |
| `RATE_LIMIT_RPM` | `10` | Maximum requests per minute per IP address (min: 1) |
| `MAX_REQUEST_SIZE` | `1048576` | Maximum request body size in bytes (min: 1024). Default is 1 MB. |
| `ALLOWED_ORIGINS` | `*` | Comma-separated CORS allowed origins, or `*` for all |
| `TRUST_PROXY` | `false` | Trust `X-Forwarded-For` / `X-Real-IP` headers for client IP detection. Set `true` behind a reverse proxy. Accepts: `true`/`1`/`yes`/`on` or `false`/`0`/`no`/`off`. |
| `ARDUINO_CLI_PATH` | `arduino-cli` | Path to the arduino-cli binary (resolved via `PATH` by default) |

---

## Architecture

### Project Structure

```
.
├── main.go                          # Entry point: config, compiler init, server start
├── go.mod                           # Go module (go 1.24, chi, x/time)
├── go.sum                           # Dependency checksums
├── Dockerfile                       # Multi-stage: Go build → Debian runtime with arduino-cli
├── docker-compose.yml               # Service config with env vars, resource limits, healthcheck
├── .dockerignore                    # Excludes .git, IDE files, docs from build context
├── .gitignore                       # Excludes binaries, coverage, IDE files, env files
├── scripts/
│   └── install-cores.sh             # Installs AVR, ESP32, ESP8266 board cores at build time
└── internal/
    ├── config/
    │   └── config.go                # Environment-based configuration with validation
    ├── compiler/
    │   ├── compiler.go              # arduino-cli wrapper: validation, semaphore, compilation
    │   ├── errors.go                # Structured error types with HTTP status mapping
    │   └── compiler_test.go         # 7 tests: validation, semaphore, boards, error interface
    └── server/
        ├── server.go                # HTTP server, chi router, endpoint handlers
        ├── middleware.go            # Recovery, logging, CORS, rate limiting, body size limit
        └── server_test.go           # 6 tests: endpoints, CORS, method validation
```

### Compilation Flow

```
Client Request
     │
     ▼
┌─────────────────────────┐
│  Middleware Stack        │
│  Recovery → Logging →   │
│  CORS → Rate Limit →   │
│  Max Body Size          │
└────────────┬────────────┘
             │
             ▼
┌─────────────────────────┐
│  Input Validation       │
│  • Code non-empty?      │
│  • Board FQBN valid?    │
└────────────┬────────────┘
             │
             ▼
┌─────────────────────────┐
│  Semaphore Acquire      │
│  Non-blocking select:   │
│  • Acquired → proceed   │
│  • Full → HTTP 429      │
└────────────┬────────────┘
             │
             ▼
┌─────────────────────────┐
│  Temp Directory Setup   │
│  • Create isolated dir  │
│  • Write sketch.ino     │
│  • Create output dir    │
└────────────┬────────────┘
             │
             ▼
┌─────────────────────────┐
│  arduino-cli compile    │
│  --fqbn <board>         │
│  --output-dir <out>     │
│  --warnings all         │
│  With context timeout   │
└────────────┬────────────┘
             │
             ▼
┌─────────────────────────┐
│  Read Binary Output     │
│  • .hex for AVR boards  │
│  • .bin for ESP boards  │
└────────────┬────────────┘
             │
             ▼
┌─────────────────────────┐
│  Cleanup & Response     │
│  • Remove temp dir      │
│  • Release semaphore    │
│  • Stream binary back   │
└─────────────────────────┘
```

### Middleware Stack

Applied in order (top-to-bottom for requests, bottom-to-top for responses):

| Order | Middleware | Purpose |
|-------|-----------|---------|
| 1 | Recovery | Catches panics, returns HTTP 500 instead of crashing |
| 2 | Logging | Logs method, path, status, duration, client IP |
| 3 | CORS | Handles preflight requests, sets origin/method/expose headers |
| 4 | Rate Limit | Per-IP token bucket with background stale entry cleanup |
| 5 | Max Body Size | Rejects oversized request bodies with HTTP 413 |

### Error Handling

The compiler returns structured `CompileError` values with a `Type` field that maps to HTTP status codes:

| Error Type | HTTP Status | When |
|-----------|-------------|------|
| `ErrValidation` | `400 Bad Request` | Empty code, unsupported board, bad JSON |
| `ErrCompilation` | `422 Unprocessable Entity` | Syntax errors, missing functions, bad includes |
| `ErrTimeout` | `408 Request Timeout` | Compilation exceeds `COMPILE_TIMEOUT` |
| `ErrCapacity` | `429 Too Many Requests` | All compilation slots are in use |
| `ErrInternal` | `500 Internal Server Error` | Filesystem failure, arduino-cli unavailable |

Compilation errors (`422`) include a `details` field with the full compiler stderr output (syntax errors, line numbers, etc.).

---

## Development

### Prerequisites

- **Go 1.24+** — for building and running locally
- **Docker** — for the full containerized setup with arduino-cli

### Running Locally (without Docker)

The Go binary can start without arduino-cli installed. It will run in degraded mode — health checks report `degraded` and compilations fail with clear errors, but the server is otherwise fully functional for development.

```bash
# Install dependencies
go mod download

# Build
go build -o arduino-compiler .

# Run (starts in degraded mode without arduino-cli)
./arduino-compiler

# With custom configuration
PORT=9090 MAX_CONCURRENT_COMPILATIONS=3 RATE_LIMIT_RPM=30 ./arduino-compiler
```

If you have arduino-cli installed locally with the required cores, compilations will work:

```bash
# Install arduino-cli (macOS)
brew install arduino-cli

# Install required cores
arduino-cli core update-index
arduino-cli config set board_manager.additional_urls \
  "https://raw.githubusercontent.com/espressif/arduino-esp32/gh-pages/package_esp32_index.json" \
  "https://arduino.esp8266.com/stable/package_esp8266com_index.json"
arduino-cli core update-index
arduino-cli core install arduino:avr
arduino-cli core install esp32:esp32
arduino-cli core install esp8266:esp8266
```

### Running Tests

Tests do not require arduino-cli. They validate input handling, error types, CORS, routing, and semaphore behavior.

```bash
# Run all tests
go test ./...

# With verbose output
go test ./... -v

# With coverage
go test ./... -cover

# Run specific package tests
go test ./internal/compiler/... -v
go test ./internal/server/... -v
```

```bash
# Static analysis
go vet ./...
```

**Test coverage:**

| Package | Tests | What's Tested |
|---------|-------|---------------|
| `internal/compiler` | 7 | Input validation (empty code, whitespace, unsupported board, empty board), semaphore capacity, supported boards map isolation, error interface |
| `internal/server` | 6 | Health endpoint, boards endpoint (sorted output), compile validation (bad JSON, empty code, unsupported board), CORS headers (preflight, regular, non-allowed origin), method not allowed |

---

## Deployment

### Docker Compose (Recommended)

```bash
# Build and start
docker compose up --build

# Detached mode
docker compose up --build -d

# View logs
docker compose logs -f

# Stop
docker compose down
```

The default `docker-compose.yml` includes resource limits (4 CPU, 4GB RAM), log rotation (10MB max, 3 files), healthchecks, and automatic restart.

### Docker CLI

```bash
# Build
docker build -t arduino-compiler .

# Run with defaults
docker run -p 8080:8080 arduino-compiler

# Run with custom settings
docker run -p 8080:8080 \
  -e MAX_CONCURRENT_COMPILATIONS=10 \
  -e COMPILE_TIMEOUT=180 \
  -e RATE_LIMIT_RPM=20 \
  -e ALLOWED_ORIGINS="https://myapp.com,https://staging.myapp.com" \
  arduino-compiler
```

### Behind a Reverse Proxy

When running behind nginx, Caddy, Cloudflare, or similar:

```bash
docker run -p 8080:8080 \
  -e TRUST_PROXY=true \
  -e ALLOWED_ORIGINS="https://myapp.com" \
  arduino-compiler
```

Setting `TRUST_PROXY=true` tells the server to use `X-Forwarded-For` and `X-Real-IP` headers for client IP detection, which is required for rate limiting to work correctly behind a proxy. When `false` (default), only the direct TCP connection address is used, preventing clients from spoofing their IP.

**Example nginx configuration:**

```nginx
upstream arduino_compiler {
    server 127.0.0.1:8080;
}

server {
    listen 443 ssl;
    server_name compiler.myapp.com;

    location / {
        proxy_pass http://arduino_compiler;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Compilation can take a while
        proxy_read_timeout 180s;
        proxy_send_timeout 180s;
    }
}
```

### Resource Requirements

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| CPU | 1 core | 4 cores |
| RAM | 1 GB | 4 GB |
| Disk | 2 GB (image) | 10 GB (image + build cache) |

The Docker image is large (~7 GB) primarily due to pre-installed board cores (ESP32 core alone is ~500 MB). The first build takes 10-15 minutes; subsequent rebuilds are fast due to Docker layer caching.

**Scale target:** ~20 concurrent users, ~200 daily users with default settings.

---

## Security

- **Non-root execution** — The container runs as an unprivileged `appuser`, not root.
- **No shell injection** — arduino-cli is invoked via Go's `exec.Command` with an argument array, not through a shell.
- **Isolated temp directories** — Each compilation gets its own temporary directory, cleaned up immediately after completion (even on errors or panics).
- **Request body limits** — Configurable maximum request size (default 1 MB) enforced at the middleware level.
- **Compilation timeout** — Runaway compilations are killed after the configured timeout (default 120s).
- **Rate limiting** — Per-IP token bucket prevents abuse. Stale entries are cleaned up automatically.
- **Proxy header protection** — `X-Forwarded-For` and `X-Real-IP` headers are only trusted when `TRUST_PROXY=true`, preventing rate limit bypass via header spoofing.
- **CORS control** — Origins can be restricted to specific domains instead of wildcard.

---

## Roadmap

### Additional Board Support

Planned boards to add in future releases:

**Arduino ARM (SAMD)**
| FQBN | Board Name |
|------|-----------|
| `arduino:samd:mkr1000` | Arduino MKR1000 |
| `arduino:samd:mkrzero` | Arduino MKR Zero |
| `arduino:samd:mkrwifi1010` | Arduino MKR WiFi 1010 |
| `arduino:samd:nano_33_iot` | Arduino Nano 33 IoT |
| `arduino:samd:arduino_zero_edbg` | Arduino Zero |

**Arduino Mbed OS (Nano 33 BLE, RP2040)**
| FQBN | Board Name |
|------|-----------|
| `arduino:mbed_nano:nano33ble` | Arduino Nano 33 BLE |
| `arduino:mbed_nano:nanorp2040connect` | Arduino Nano RP2040 Connect |
| `arduino:mbed_rp2040:pico` | Raspberry Pi Pico |

**Adafruit (SAMD, nRF52)**
| FQBN | Board Name |
|------|-----------|
| `adafruit:samd:adafruit_feather_m0` | Adafruit Feather M0 |
| `adafruit:samd:adafruit_feather_m4` | Adafruit Feather M4 Express |
| `adafruit:samd:adafruit_qtpy_m0` | Adafruit QT Py |
| `adafruit:nrf52:feather52832` | Adafruit Feather nRF52832 |
| `adafruit:nrf52:feather52840` | Adafruit Feather nRF52840 Express |

**Teensy**
| FQBN | Board Name |
|------|-----------|
| `teensy:avr:teensy41` | Teensy 4.1 |
| `teensy:avr:teensy40` | Teensy 4.0 |
| `teensy:avr:teensyLC` | Teensy LC |

**STM32**
| FQBN | Board Name |
|------|-----------|
| `STMicroelectronics:stm32:GenF4` | STM32F4 Discovery |
| `STMicroelectronics:stm32:Nucleo_64` | STM32 Nucleo-64 |

**Seeed Studio**
| FQBN | Board Name |
|------|-----------|
| `Seeeduino:samd:seeed_XIAO_m0` | Seeeduino XIAO |
| `Seeeduino:samd:seeed_wio_terminal` | Wio Terminal |

Adding a new board requires:
1. Adding the FQBN and name to the `supportedBoards` map in `internal/compiler/compiler.go`
2. Adding the core's board manager URL (if third-party) to `scripts/install-cores.sh`
3. Installing the core in `scripts/install-cores.sh`
4. Rebuilding the Docker image

### Library Management

Planned features for Arduino library support:

- **`POST /api/libraries/install`** — Install Arduino libraries by name or URL at runtime or build time
- **`GET /api/libraries`** — List installed libraries
- **Built-in popular libraries** — Pre-install commonly used libraries into the Docker image:
  - `Adafruit_NeoPixel` — Addressable LED control
  - `Servo` — Servo motor control
  - `Wire` / `SPI` — Communication protocols
  - `FastLED` — High-performance LED library
  - `PubSubClient` — MQTT client for IoT
  - `ArduinoJson` — JSON parsing and serialization
  - `WiFi` / `WiFiClient` — Network connectivity (ESP boards)
  - `Adafruit_SSD1306` / `U8g2` — OLED display drivers
  - `DHT` — Temperature/humidity sensor library
  - `IRremote` — Infrared remote control
  - `AccelStepper` — Stepper motor control
  - `LiquidCrystal_I2C` — I2C LCD display driver
- **Per-request library specification** — Allow the compile request to declare dependencies:
  ```json
  {
    "code": "...",
    "board": "arduino:avr:uno",
    "libraries": ["Adafruit_NeoPixel@1.12.0", "ArduinoJson@7.0.0"]
  }
  ```
- **Library caching** — Cache downloaded libraries across compilations to avoid repeated downloads
- **Custom library upload** — Accept `.zip` library uploads for proprietary or unpublished libraries

### Compilation Features

- **Compiler flags** — Allow custom `-D` defines and compiler flags per request
- **Board options** — Support board-specific menu options (e.g., CPU frequency, flash size, partition scheme for ESP32)
  ```json
  {
    "code": "...",
    "board": "esp32:esp32:esp32",
    "options": { "FlashSize": "16M", "PartitionScheme": "huge_app" }
  }
  ```
- **Multi-file sketches** — Accept multiple source files (`.ino`, `.h`, `.cpp`) in a single request
- **Compilation output streaming** — Stream compiler output in real-time via WebSocket or SSE for progress feedback
- **Binary size reporting** — Return flash/RAM usage statistics alongside the binary
  ```json
  {
    "flash_used": 924,
    "flash_total": 32256,
    "ram_used": 9,
    "ram_total": 2048
  }
  ```
- **Warnings-only mode** — Return compilation warnings without building the full binary (faster feedback)
- **Cached compilation** — Hash-based caching: skip recompilation when the same code + board + libraries combination is requested again

### Platform & Infrastructure

- **WebSocket support** — Real-time compilation progress and log streaming
- **Web Serial integration** — Browser-based firmware upload using the Web Serial API (compile → upload in one flow)
- **Compilation queue** — Replace the semaphore with a proper job queue (Redis, RabbitMQ) for better fairness and observability
- **Horizontal scaling** — Stateless design already supports running multiple instances behind a load balancer
- **Prometheus metrics** — Export compilation counts, durations, error rates, queue depth, and cache hit rates
- **Structured logging** — Switch from `log.Printf` to structured JSON logging (zerolog or slog) for better observability
- **Graceful shutdown** — Handle `SIGTERM` / `SIGINT` to drain in-flight compilations before stopping
- **API versioning** — Version the API (`/v1/compile`, `/v2/compile`) for backward-compatible evolution
- **Authentication** — Optional API key or JWT authentication for private deployments
- **Usage quotas** — Per-user or per-API-key compilation quotas (daily/monthly limits)
- **Webhook notifications** — Notify external services when compilations complete (useful for CI/CD pipelines)

### Developer Experience

- **Interactive API docs** — Swagger/OpenAPI specification with a bundled Swagger UI at `/docs`
- **SDK generation** — Auto-generated client SDKs for JavaScript/TypeScript, Python, and Go
- **CLI client** — A companion CLI tool for compiling and uploading from the terminal:
  ```bash
  derivative compile sketch.ino --board arduino:avr:uno --upload /dev/ttyUSB0
  ```
- **VS Code extension** — Compile and upload directly from VS Code without the Arduino IDE
- **Example sketches** — Bundled example sketches accessible via `GET /api/examples`

---

## Contributing

Contributions are welcome. To get started:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Make your changes
4. Run tests (`go test ./... && go vet ./...`)
5. Commit and push
6. Open a pull request

When adding a new board, update both `internal/compiler/compiler.go` (the `supportedBoards` map) and `scripts/install-cores.sh` (the core installation). Rebuild the Docker image and test compilation for the new board before submitting.

---

## License

This project is open source. See the [LICENSE](LICENSE) file for details.
