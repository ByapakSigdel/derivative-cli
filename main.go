// Arduino Compiler API Server
//
// This is the entry point for the Arduino compiler backend service. It provides
// a REST API that accepts raw Arduino C++ sketch code and returns compiled
// firmware binaries (.hex for AVR, .bin for ESP32/ESP8266) that can be directly
// uploaded to Arduino-compatible boards from a browser.
//
// The server eliminates the need for users to install the Arduino IDE or
// arduino-cli locally. Code is compiled server-side inside a Docker container
// that has the full Arduino toolchain pre-installed.
//
// # Quick Start
//
//	docker compose up --build
//
// # API Endpoints
//
//	GET  /health       — Server health, arduino-cli version, installed cores.
//	GET  /api/boards   — List supported board FQBNs.
//	POST /api/compile  — Compile Arduino code and download the binary.
//
// # Configuration
//
// All settings are controlled via environment variables. See internal/config
// for the full list and defaults.
//
// # Example Compilation Request
//
//	curl -X POST http://localhost:8080/api/compile \
//	  -H "Content-Type: application/json" \
//	  -d '{"code":"void setup(){} void loop(){}","board":"arduino:avr:uno"}' \
//	  --output sketch.hex
package main

import (
	"log"

	"github.com/derivative-cli/arduino-compiler/internal/compiler"
	"github.com/derivative-cli/arduino-compiler/internal/config"
	"github.com/derivative-cli/arduino-compiler/internal/server"
)

func main() {
	// Load configuration from environment variables.
	cfg := config.Load()

	// Initialize the compiler (wraps arduino-cli).
	comp := compiler.New(cfg)

	// Check that arduino-cli is available. This is not fatal — the health
	// endpoint will report "degraded" status, and compile requests will fail
	// with a clear error message. This allows the Go binary to start even
	// in development environments without arduino-cli installed.
	version, err := comp.CheckArduinoCLI()
	if err != nil {
		log.Printf("[WARN] arduino-cli is not available: %v", err)
		log.Printf("[WARN] The server will start, but compilations will fail.")
		log.Printf("[WARN] Ensure arduino-cli is installed and in PATH.")
	} else {
		log.Printf("[INFO] %s", version)

		// Log installed cores.
		cores, err := comp.ListInstalledCores()
		if err != nil {
			log.Printf("[WARN] Failed to list installed cores: %v", err)
		} else {
			log.Printf("[INFO] Installed cores:\n%s", cores)
		}
	}

	// Create and start the HTTP server.
	srv := server.New(cfg, comp)

	log.Fatal(srv.Start())
}
