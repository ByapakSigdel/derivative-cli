// Package compiler wraps the arduino-cli tool to provide Arduino sketch compilation.
//
// It manages the full lifecycle of a compilation request:
//  1. Creates an isolated temporary directory for the sketch.
//  2. Writes the user's C++ code into a properly named .ino file.
//  3. Invokes arduino-cli compile with the specified board FQBN.
//  4. Reads the compiled binary output (.hex or .bin).
//  5. Cleans up the temporary directory.
//
// The compiler enforces a per-compilation timeout and uses a semaphore to limit
// the number of concurrent compilations, preventing resource exhaustion.
package compiler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/derivative-cli/arduino-compiler/internal/cache"
	"github.com/derivative-cli/arduino-compiler/internal/config"
)

// supportedBoards maps Fully Qualified Board Names (FQBNs) to human-readable
// descriptions. Only boards in this map are accepted for compilation. The
// corresponding board cores must be installed via arduino-cli at build time.
var supportedBoards = map[string]string{
	// Arduino AVR boards
	"arduino:avr:uno":       "Arduino Uno",
	"arduino:avr:mega":      "Arduino Mega 2560",
	"arduino:avr:nano":      "Arduino Nano",
	"arduino:avr:leonardo":  "Arduino Leonardo",
	"arduino:avr:micro":     "Arduino Micro",
	"arduino:avr:pro":       "Arduino Pro / Pro Mini",
	"arduino:avr:diecimila": "Arduino Duemilanove / Diecimila",

	// ESP32 boards
	"esp32:esp32:esp32":       "ESP32 Dev Module",
	"esp32:esp32:esp32s3":     "ESP32-S3 Dev Module",
	"esp32:esp32:esp32c3":     "ESP32-C3 Dev Module",
	"esp32:esp32:esp32wrover": "ESP32 Wrover Module",

	// ESP8266 boards
	"esp8266:esp8266:generic":      "ESP8266 Generic Module",
	"esp8266:esp8266:nodemcuv2":    "NodeMCU 1.0 (ESP-12E)",
	"esp8266:esp8266:d1_mini":      "LOLIN (Wemos) D1 Mini",
	"esp8266:esp8266:d1_mini_pro":  "LOLIN (Wemos) D1 Mini Pro",
	"esp8266:esp8266:d1_mini_lite": "LOLIN (Wemos) D1 Mini Lite",
}

// CompileRequest holds the parameters for a compilation request.
type CompileRequest struct {
	// Code is the raw Arduino C++ sketch source code.
	Code string `json:"code"`

	// Board is the Fully Qualified Board Name (FQBN) for the target board.
	// Example: "arduino:avr:uno", "esp32:esp32:esp32"
	Board string `json:"board"`
}

// CompileResult holds the output of a successful compilation.
type CompileResult struct {
	// Binary contains the compiled firmware bytes (.hex for AVR, .bin for ESP).
	Binary []byte

	// Filename is the suggested filename for the binary download.
	// Example: "sketch.hex" for AVR boards, "sketch.bin" for ESP boards.
	Filename string

	// Stdout contains the standard output from the compilation process.
	Stdout string

	// BoardName is the human-readable name of the target board.
	BoardName string

	// Cached is true when Binary was served from the cache rather than freshly
	// compiled. The handler surfaces this as the X-Cache response header.
	Cached bool
}

// Compiler manages Arduino sketch compilations using arduino-cli.
type Compiler struct {
	cfg       *config.Config
	semaphore chan struct{} // Limits concurrent compilations.
	cache     cache.Cache

	// Observability counters (lifetime totals), exposed via CacheStats.
	cacheHits   atomic.Uint64
	cacheMisses atomic.Uint64
}

// New creates a new Compiler with the given configuration and compile cache.
// The semaphore channel is sized to cfg.MaxConcurrentCompilations. Pass a nil
// cache to disable caching (a no-op cache is substituted).
func New(cfg *config.Config, c cache.Cache) *Compiler {
	if c == nil {
		c = cache.NewNoop()
	}
	return &Compiler{
		cfg:       cfg,
		semaphore: make(chan struct{}, cfg.MaxConcurrentCompilations),
		cache:     c,
	}
}

// CacheStats returns the lifetime cache hit and miss counts.
func (c *Compiler) CacheStats() (hits, misses uint64) {
	return c.cacheHits.Load(), c.cacheMisses.Load()
}

// cacheKey derives a stable, collision-resistant key from the board + code.
// The CacheVersion prefix lets us invalidate the whole cache when the toolchain
// changes (see config.Config.CacheVersion).
func (c *Compiler) cacheKey(board, code string) string {
	h := sha256.New()
	h.Write([]byte(board))
	h.Write([]byte{0}) // separator so board||code can't collide across the boundary
	h.Write([]byte(code))
	return fmt.Sprintf("compile:%s:%x", c.cfg.CacheVersion, h.Sum(nil))
}

// filenameForBoard returns the download filename for a board's compiled output.
// AVR boards emit Intel HEX; ESP boards emit raw binary. This mirrors the
// extension logic in findCompiledBinary and lets a cache hit reconstruct the
// filename without storing it alongside the binary.
func filenameForBoard(fqbn string) string {
	if strings.HasPrefix(fqbn, "esp32:") || strings.HasPrefix(fqbn, "esp8266:") {
		return "sketch.bin"
	}
	// AVR (and any other supported board) produces .hex.
	return "sketch.hex"
}

// Compile compiles the given Arduino sketch code for the specified board.
//
// It acquires a semaphore slot before starting (returns an error if the server
// is at capacity), creates an isolated temp directory, writes the sketch,
// invokes arduino-cli, and returns the compiled binary.
//
// The ctx parameter should carry the HTTP request context so that client
// disconnections cancel the compilation.
func (c *Compiler) Compile(ctx context.Context, req CompileRequest) (*CompileResult, error) {
	// --- Validate inputs ---
	if strings.TrimSpace(req.Code) == "" {
		return nil, &CompileError{
			Type:    ErrValidation,
			Message: "code field is required and cannot be empty",
		}
	}

	boardName, ok := supportedBoards[req.Board]
	if !ok {
		return nil, &CompileError{
			Type:    ErrValidation,
			Message: fmt.Sprintf("unsupported board FQBN: %q. Use GET /api/boards for supported boards", req.Board),
		}
	}

	// --- Cache lookup (before the semaphore) ---
	// A hit lets us skip both arduino-cli and the concurrency semaphore, so a
	// cached request is served instantly even while a real compile holds the
	// only slot. We look up after validation so we never key on bad input.
	cacheKey := c.cacheKey(req.Board, req.Code)
	if cached, hit := c.cache.Get(ctx, cacheKey); hit {
		c.cacheHits.Add(1)
		return &CompileResult{
			Binary:    cached,
			Filename:  filenameForBoard(req.Board),
			BoardName: boardName,
			Cached:    true,
		}, nil
	}
	c.cacheMisses.Add(1)

	// --- Acquire semaphore (non-blocking) ---
	select {
	case c.semaphore <- struct{}{}:
		defer func() { <-c.semaphore }()
	default:
		return nil, &CompileError{
			Type:    ErrCapacity,
			Message: "server is at maximum compilation capacity, please try again shortly",
		}
	}

	// --- Create isolated temp directory ---
	// arduino-cli requires the sketch directory and .ino file to share the same name.
	tmpDir, err := os.MkdirTemp("", "arduino-compile-*")
	if err != nil {
		return nil, &CompileError{
			Type:    ErrInternal,
			Message: fmt.Sprintf("failed to create temp directory: %v", err),
		}
	}
	defer os.RemoveAll(tmpDir) // Always clean up.

	sketchName := "sketch"
	sketchDir := filepath.Join(tmpDir, sketchName)
	if err := os.Mkdir(sketchDir, 0755); err != nil {
		return nil, &CompileError{
			Type:    ErrInternal,
			Message: fmt.Sprintf("failed to create sketch directory: %v", err),
		}
	}

	// Write the sketch file.
	sketchFile := filepath.Join(sketchDir, sketchName+".ino")
	if err := os.WriteFile(sketchFile, []byte(req.Code), 0644); err != nil {
		return nil, &CompileError{
			Type:    ErrInternal,
			Message: fmt.Sprintf("failed to write sketch file: %v", err),
		}
	}

	// --- Prepare output directory ---
	outputDir := filepath.Join(tmpDir, "output")
	if err := os.Mkdir(outputDir, 0755); err != nil {
		return nil, &CompileError{
			Type:    ErrInternal,
			Message: fmt.Sprintf("failed to create output directory: %v", err),
		}
	}

	// --- Build arduino-cli command ---
	compileCtx, cancel := context.WithTimeout(ctx, c.cfg.CompileTimeout)
	defer cancel()

	args := []string{
		"compile",
		"--fqbn", req.Board,
		"--output-dir", outputDir,
		"--warnings", "all",
		sketchDir,
	}

	cmd := exec.CommandContext(compileCtx, c.cfg.ArduinoCLIPath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// --- Run compilation ---
	if err := cmd.Run(); err != nil {
		// Check if the context timed out.
		if compileCtx.Err() == context.DeadlineExceeded {
			return nil, &CompileError{
				Type:    ErrTimeout,
				Message: fmt.Sprintf("compilation timed out after %v", c.cfg.CompileTimeout),
			}
		}

		// Check if the client cancelled.
		if compileCtx.Err() == context.Canceled {
			return nil, &CompileError{
				Type:    ErrInternal,
				Message: "compilation cancelled",
			}
		}

		// Compilation error (syntax errors, missing libraries, etc.).
		return nil, &CompileError{
			Type:    ErrCompilation,
			Message: "compilation failed",
			Details: combinedOutput(stdout.String(), stderr.String()),
		}
	}

	// --- Find and read the compiled binary ---
	binary, filename, err := findCompiledBinary(outputDir, req.Board)
	if err != nil {
		return nil, &CompileError{
			Type:    ErrInternal,
			Message: fmt.Sprintf("compilation succeeded but failed to read output: %v", err),
		}
	}

	// --- Store in cache (best-effort) ---
	// Only successful binaries are cached. Use a detached, short-lived context
	// so a slow SET can't extend the request and a client disconnect (which
	// cancels ctx) can't abort writing a binary we already built.
	storeCtx, storeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer storeCancel()
	c.cache.Set(storeCtx, cacheKey, binary, c.cfg.CacheTTL)

	return &CompileResult{
		Binary:    binary,
		Filename:  filename,
		Stdout:    stdout.String(),
		BoardName: boardName,
	}, nil
}

// GetSupportedBoards returns a copy of the supported boards map.
func GetSupportedBoards() map[string]string {
	boards := make(map[string]string, len(supportedBoards))
	for k, v := range supportedBoards {
		boards[k] = v
	}
	return boards
}

// CheckArduinoCLI verifies that arduino-cli is installed and accessible.
// It returns the version string on success.
func (c *Compiler) CheckArduinoCLI() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.cfg.ArduinoCLIPath, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("arduino-cli not found or not executable: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// ListInstalledCores returns the list of installed board cores by running
// arduino-cli core list.
func (c *Compiler) ListInstalledCores() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.cfg.ArduinoCLIPath, "core", "list")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to list installed cores: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// findCompiledBinary locates the compiled binary file in the output directory.
// AVR boards produce .hex files, ESP32/ESP8266 boards produce .bin files.
func findCompiledBinary(outputDir, fqbn string) ([]byte, string, error) {
	// Determine the expected file extension based on the board platform.
	var extensions []string
	if strings.HasPrefix(fqbn, "arduino:avr:") {
		extensions = []string{".hex"}
	} else if strings.HasPrefix(fqbn, "esp32:") || strings.HasPrefix(fqbn, "esp8266:") {
		extensions = []string{".bin"}
	} else {
		// Fallback: try both.
		extensions = []string{".hex", ".bin"}
	}

	// Search for compiled output files.
	for _, ext := range extensions {
		// arduino-cli names output as <sketch>.ino<ext> in the output dir.
		pattern := filepath.Join(outputDir, "*"+ext)
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}

		if len(matches) > 0 {
			// Use the first matching file.
			data, err := os.ReadFile(matches[0])
			if err != nil {
				return nil, "", fmt.Errorf("failed to read compiled binary %s: %w", matches[0], err)
			}

			filename := "sketch" + ext
			return data, filename, nil
		}
	}

	// List directory contents for debugging.
	entries, _ := os.ReadDir(outputDir)
	var files []string
	for _, e := range entries {
		files = append(files, e.Name())
	}

	return nil, "", fmt.Errorf("no compiled binary found in output directory. Files present: %v", files)
}

// combinedOutput merges stdout and stderr into a single string for error reporting.
func combinedOutput(stdout, stderr string) string {
	var parts []string
	if stdout = strings.TrimSpace(stdout); stdout != "" {
		parts = append(parts, stdout)
	}
	if stderr = strings.TrimSpace(stderr); stderr != "" {
		parts = append(parts, stderr)
	}
	return strings.Join(parts, "\n")
}
