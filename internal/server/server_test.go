package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/derivative-cli/arduino-compiler/internal/compiler"
	"github.com/derivative-cli/arduino-compiler/internal/config"
)

// testServer creates a server instance for testing.
func testServer() *Server {
	cfg := &config.Config{
		Port:                      "8080",
		MaxConcurrentCompilations: 2,
		CompileTimeout:            30 * time.Second,
		RateLimitRPM:              100, // High limit for tests.
		MaxRequestSize:            1048576,
		ArduinoCLIPath:            "arduino-cli",
		AllowedOrigins:            "*",
		TrustProxy:                false,
	}
	comp := compiler.New(cfg)
	return New(cfg, comp)
}

// TestHealthEndpoint tests the GET /health endpoint.
func TestHealthEndpoint(t *testing.T) {
	srv := testServer()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", contentType)
	}

	var resp healthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Status should be either "ok" or "degraded" (depending on arduino-cli availability).
	if resp.Status != "ok" && resp.Status != "degraded" {
		t.Errorf("expected status 'ok' or 'degraded', got %q", resp.Status)
	}

	if resp.Uptime == "" {
		t.Error("expected non-empty uptime field")
	}
}

// TestBoardsEndpoint tests the GET /api/boards endpoint.
func TestBoardsEndpoint(t *testing.T) {
	srv := testServer()

	req := httptest.NewRequest(http.MethodGet, "/api/boards", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp boardsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Count == 0 {
		t.Error("expected non-zero board count")
	}

	if len(resp.Boards) != resp.Count {
		t.Errorf("board count mismatch: boards=%d, count=%d", len(resp.Boards), resp.Count)
	}

	// Verify boards are sorted by FQBN.
	for i := 1; i < len(resp.Boards); i++ {
		if resp.Boards[i].FQBN < resp.Boards[i-1].FQBN {
			t.Errorf("boards not sorted: %q comes after %q", resp.Boards[i].FQBN, resp.Boards[i-1].FQBN)
		}
	}
}

// TestCompileEndpointValidation tests the POST /api/compile endpoint with
// invalid inputs.
func TestCompileEndpointValidation(t *testing.T) {
	srv := testServer()

	t.Run("invalid JSON body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/compile", bytes.NewBufferString("not json"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		srv.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", w.Code)
		}
	})

	t.Run("empty code", func(t *testing.T) {
		body := `{"code":"","board":"arduino:avr:uno"}`
		req := httptest.NewRequest(http.MethodPost, "/api/compile", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		srv.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", w.Code)
		}

		var resp compileErrorResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if resp.Error != "validation_error" {
			t.Errorf("expected error type 'validation_error', got %q", resp.Error)
		}
	})

	t.Run("unsupported board", func(t *testing.T) {
		body := `{"code":"void setup(){} void loop(){}","board":"fake:board:fqbn"}`
		req := httptest.NewRequest(http.MethodPost, "/api/compile", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		srv.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", w.Code)
		}
	})
}

// TestCORSHeaders tests that CORS headers are set correctly.
func TestCORSHeaders(t *testing.T) {
	srv := testServer()

	t.Run("preflight request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/api/compile", nil)
		req.Header.Set("Origin", "http://example.com")
		w := httptest.NewRecorder()

		srv.ServeHTTP(w, req)

		if w.Code != http.StatusNoContent {
			t.Errorf("expected status 204, got %d", w.Code)
		}

		if w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("expected CORS allow-origin *, got %q", w.Header().Get("Access-Control-Allow-Origin"))
		}

		if w.Header().Get("Access-Control-Allow-Methods") == "" {
			t.Error("expected non-empty Access-Control-Allow-Methods header")
		}

		// Verify expose headers are set so browser JS can read custom response headers.
		exposeHeaders := w.Header().Get("Access-Control-Expose-Headers")
		if exposeHeaders == "" {
			t.Error("expected non-empty Access-Control-Expose-Headers header")
		}
		for _, expected := range []string{"Content-Disposition", "X-Board-Name", "X-Filename", "Content-Length"} {
			if !strings.Contains(exposeHeaders, expected) {
				t.Errorf("expected Access-Control-Expose-Headers to contain %q, got %q", expected, exposeHeaders)
			}
		}
	})

	t.Run("regular request has CORS headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Header.Set("Origin", "http://example.com")
		w := httptest.NewRecorder()

		srv.ServeHTTP(w, req)

		if w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("expected CORS allow-origin *, got %q", w.Header().Get("Access-Control-Allow-Origin"))
		}
	})

	t.Run("non-allowed origin does not get CORS headers", func(t *testing.T) {
		// Create a server with specific allowed origins.
		cfg := &config.Config{
			Port:                      "8080",
			MaxConcurrentCompilations: 2,
			CompileTimeout:            30 * time.Second,
			RateLimitRPM:              100,
			MaxRequestSize:            1048576,
			ArduinoCLIPath:            "arduino-cli",
			AllowedOrigins:            "http://allowed.com",
			TrustProxy:                false,
		}
		comp := compiler.New(cfg)
		restrictedSrv := New(cfg, comp)

		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Header.Set("Origin", "http://evil.com")
		w := httptest.NewRecorder()

		restrictedSrv.ServeHTTP(w, req)

		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("expected no CORS allow-origin for non-allowed origin, got %q",
				w.Header().Get("Access-Control-Allow-Origin"))
		}
		if w.Header().Get("Access-Control-Allow-Methods") != "" {
			t.Errorf("expected no CORS allow-methods for non-allowed origin, got %q",
				w.Header().Get("Access-Control-Allow-Methods"))
		}
	})
}

// TestMethodNotAllowed tests that incorrect HTTP methods return appropriate errors.
func TestMethodNotAllowed(t *testing.T) {
	srv := testServer()

	req := httptest.NewRequest(http.MethodGet, "/api/compile", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	// chi returns 405 Method Not Allowed for wrong methods on existing routes.
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}
