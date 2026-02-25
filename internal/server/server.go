// Package server provides the HTTP server, routing, and request handling for
// the Arduino compiler API.
//
// # Endpoints
//
//	GET  /health       — Health check with arduino-cli status and installed cores.
//	GET  /api/boards   — Lists all supported board FQBNs.
//	POST /api/compile  — Compiles Arduino C++ code and returns the binary.
//
// # Architecture
//
// The server uses the chi router for clean middleware chaining and route
// definitions. Each request to /api/compile creates an isolated temporary
// directory, invokes arduino-cli as a subprocess, and streams the compiled
// binary back to the client.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/derivative-cli/arduino-compiler/internal/compiler"
	"github.com/derivative-cli/arduino-compiler/internal/config"
)

// Server holds the HTTP server dependencies.
type Server struct {
	cfg      *config.Config
	compiler *compiler.Compiler
	router   *chi.Mux
}

// New creates a new Server with the given configuration and compiler.
// It sets up all routes and middleware.
func New(cfg *config.Config, comp *compiler.Compiler) *Server {
	s := &Server{
		cfg:      cfg,
		compiler: comp,
		router:   chi.NewRouter(),
	}

	s.setupMiddleware()
	s.setupRoutes()

	return s
}

// setupMiddleware applies the middleware stack in order.
// Middleware executes top-to-bottom for requests and bottom-to-top for responses.
func (s *Server) setupMiddleware() {
	s.router.Use(recoveryMiddleware)
	s.router.Use(loggingMiddleware(s.cfg.TrustProxy))
	s.router.Use(corsMiddleware(s.cfg.AllowedOrigins))
	s.router.Use(rateLimitMiddleware(s.cfg))
	s.router.Use(maxBodySizeMiddleware(s.cfg.MaxRequestSize))
}

// setupRoutes registers all HTTP endpoint handlers.
func (s *Server) setupRoutes() {
	s.router.Get("/health", s.handleHealth)
	s.router.Get("/api/boards", s.handleListBoards)
	s.router.Post("/api/compile", s.handleCompile)
}

// ServeHTTP delegates to the chi router, implementing the http.Handler interface.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// Start begins listening for HTTP requests on the configured port.
// It blocks until the server is shut down or encounters a fatal error.
func (s *Server) Start() error {
	addr := ":" + s.cfg.Port
	log.Printf("[SERVER] Starting Arduino Compiler API on %s", addr)
	log.Printf("[SERVER] Configuration:")
	log.Printf("[SERVER]   Max concurrent compilations: %d", s.cfg.MaxConcurrentCompilations)
	log.Printf("[SERVER]   Compile timeout: %v", s.cfg.CompileTimeout)
	log.Printf("[SERVER]   Rate limit: %d req/min per IP", s.cfg.RateLimitRPM)
	log.Printf("[SERVER]   Max request size: %d bytes", s.cfg.MaxRequestSize)
	log.Printf("[SERVER]   CORS allowed origins: %s", s.cfg.AllowedOrigins)
	log.Printf("[SERVER]   Trust proxy headers: %v", s.cfg.TrustProxy)

	srv := &http.Server{
		Addr:         addr,
		Handler:      s,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: s.cfg.CompileTimeout + 30*time.Second, // Compilation time + buffer.
		IdleTimeout:  120 * time.Second,
	}

	return srv.ListenAndServe()
}

// --- Handler: Health Check ---

// healthResponse is the JSON response body for the health check endpoint.
type healthResponse struct {
	Status         string `json:"status"`
	ArduinoCLI     string `json:"arduino_cli"`
	InstalledCores string `json:"installed_cores"`
	Uptime         string `json:"uptime"`
}

// serverStartTime records when the server was initialized, for uptime reporting.
var serverStartTime = time.Now()

// handleHealth responds with the server's health status, including the
// arduino-cli version and installed board cores.
//
//	GET /health
//
// Response 200:
//
//	{
//	  "status": "ok",
//	  "arduino_cli": "arduino-cli Version: 1.x.x ...",
//	  "installed_cores": "...",
//	  "uptime": "2h30m15s"
//	}
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{
		Status: "ok",
		Uptime: time.Since(serverStartTime).Round(time.Second).String(),
	}

	// Check arduino-cli availability.
	version, err := s.compiler.CheckArduinoCLI()
	if err != nil {
		resp.Status = "degraded"
		resp.ArduinoCLI = fmt.Sprintf("error: %v", err)
	} else {
		resp.ArduinoCLI = version
	}

	// List installed cores.
	cores, err := s.compiler.ListInstalledCores()
	if err != nil {
		resp.InstalledCores = fmt.Sprintf("error: %v", err)
	} else {
		resp.InstalledCores = cores
	}

	writeJSON(w, http.StatusOK, resp)
}

// --- Handler: List Boards ---

// boardEntry represents a single supported board in the list response.
type boardEntry struct {
	FQBN string `json:"fqbn"`
	Name string `json:"name"`
}

// boardsResponse is the JSON response body for the boards list endpoint.
type boardsResponse struct {
	Boards []boardEntry `json:"boards"`
	Count  int          `json:"count"`
}

// handleListBoards returns the list of all supported board FQBNs and their
// human-readable names, sorted alphabetically by FQBN.
//
//	GET /api/boards
//
// Response 200:
//
//	{
//	  "boards": [
//	    {"fqbn": "arduino:avr:uno", "name": "Arduino Uno"},
//	    ...
//	  ],
//	  "count": 15
//	}
func (s *Server) handleListBoards(w http.ResponseWriter, r *http.Request) {
	boards := compiler.GetSupportedBoards()

	entries := make([]boardEntry, 0, len(boards))
	for fqbn, name := range boards {
		entries = append(entries, boardEntry{FQBN: fqbn, Name: name})
	}

	// Sort by FQBN for consistent output.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].FQBN < entries[j].FQBN
	})

	writeJSON(w, http.StatusOK, boardsResponse{
		Boards: entries,
		Count:  len(entries),
	})
}

// --- Handler: Compile ---

// compileRequest is the JSON request body for the compile endpoint.
type compileRequest struct {
	// Code is the raw Arduino C++ sketch source code.
	Code string `json:"code"`

	// Board is the Fully Qualified Board Name (FQBN) for the target board.
	// Example: "arduino:avr:uno"
	Board string `json:"board"`
}

// compileErrorResponse is the JSON response body for compilation errors.
type compileErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// handleCompile accepts a JSON body with Arduino C++ code and a board FQBN,
// compiles the code using arduino-cli, and returns the compiled binary.
//
//	POST /api/compile
//	Content-Type: application/json
//
//	{
//	  "code": "void setup() {} void loop() {}",
//	  "board": "arduino:avr:uno"
//	}
//
// Response 200 (success):
//
//	Content-Type: application/octet-stream
//	Content-Disposition: attachment; filename="sketch.hex"
//	X-Board-Name: Arduino Uno
//	<binary data>
//
// Response 400/408/422/429/500 (error):
//
//	{
//	  "error": "compilation failed",
//	  "message": "...",
//	  "details": "compiler stderr output..."
//	}
func (s *Server) handleCompile(w http.ResponseWriter, r *http.Request) {
	// Parse the JSON request body.
	var req compileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, compileErrorResponse{
			Error:   "invalid_request",
			Message: fmt.Sprintf("failed to parse JSON request body: %v", err),
		})
		return
	}

	// Compile the sketch.
	result, err := s.compiler.Compile(r.Context(), compiler.CompileRequest{
		Code:  req.Code,
		Board: req.Board,
	})

	if err != nil {
		// Map CompileError types to HTTP status codes.
		var compErr *compiler.CompileError
		if errors.As(err, &compErr) {
			status := mapErrorToStatus(compErr.Type)
			writeJSON(w, status, compileErrorResponse{
				Error:   errorTypeToString(compErr.Type),
				Message: compErr.Message,
				Details: compErr.Details,
			})
			return
		}

		// Unexpected error.
		log.Printf("[ERROR] Unexpected compilation error: %v", err)
		writeJSON(w, http.StatusInternalServerError, compileErrorResponse{
			Error:   "internal_error",
			Message: "an unexpected error occurred during compilation",
		})
		return
	}

	// Return the compiled binary.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, result.Filename))
	w.Header().Set("X-Board-Name", result.BoardName)
	w.Header().Set("X-Filename", result.Filename)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(result.Binary)))
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(result.Binary); err != nil {
		log.Printf("[ERROR] Failed to write response body: %v", err)
	}
}

// --- Helpers ---

// writeJSON serializes data as JSON and writes it to the response writer
// with the given HTTP status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[ERROR] Failed to encode JSON response: %v", err)
	}
}

// mapErrorToStatus maps a CompileError type to an HTTP status code.
func mapErrorToStatus(errType compiler.ErrorType) int {
	switch errType {
	case compiler.ErrValidation:
		return http.StatusBadRequest // 400
	case compiler.ErrCompilation:
		return http.StatusUnprocessableEntity // 422
	case compiler.ErrTimeout:
		return http.StatusRequestTimeout // 408
	case compiler.ErrCapacity:
		return http.StatusTooManyRequests // 429
	case compiler.ErrInternal:
		return http.StatusInternalServerError // 500
	default:
		return http.StatusInternalServerError
	}
}

// errorTypeToString returns a machine-readable string for the error type,
// suitable for use in JSON error responses.
func errorTypeToString(errType compiler.ErrorType) string {
	switch errType {
	case compiler.ErrValidation:
		return "validation_error"
	case compiler.ErrCompilation:
		return "compilation_error"
	case compiler.ErrTimeout:
		return "timeout_error"
	case compiler.ErrCapacity:
		return "capacity_error"
	case compiler.ErrInternal:
		return "internal_error"
	default:
		return "unknown_error"
	}
}
