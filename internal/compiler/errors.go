package compiler

import "fmt"

// ErrorType categorizes compilation errors so the HTTP handler can map them
// to appropriate status codes.
type ErrorType int

const (
	// ErrValidation indicates the request was malformed or contained invalid
	// parameters (e.g., empty code, unsupported board FQBN).
	// Maps to HTTP 400 Bad Request.
	ErrValidation ErrorType = iota

	// ErrCompilation indicates the code failed to compile due to syntax errors,
	// missing libraries, or other issues reported by arduino-cli.
	// Maps to HTTP 422 Unprocessable Entity.
	ErrCompilation

	// ErrTimeout indicates the compilation exceeded the configured time limit.
	// Maps to HTTP 408 Request Timeout.
	ErrTimeout

	// ErrCapacity indicates the server has reached its maximum concurrent
	// compilation limit.
	// Maps to HTTP 429 Too Many Requests.
	ErrCapacity

	// ErrInternal indicates an unexpected server-side error (filesystem issues,
	// arduino-cli not found, etc.).
	// Maps to HTTP 500 Internal Server Error.
	ErrInternal
)

// CompileError is a structured error returned by the Compiler. It carries
// a type for HTTP status code mapping, a user-facing message, and optional
// detailed output from the compiler.
type CompileError struct {
	// Type categorizes the error for HTTP status code mapping.
	Type ErrorType

	// Message is a human-readable summary of the error.
	Message string

	// Details contains extended error information, typically the compiler's
	// stderr output. This is only populated for ErrCompilation errors.
	Details string
}

// Error implements the error interface.
func (e *CompileError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("%s: %s", e.Message, e.Details)
	}
	return e.Message
}
