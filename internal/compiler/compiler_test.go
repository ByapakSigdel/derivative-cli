package compiler

import (
	"context"
	"testing"
	"time"

	"github.com/derivative-cli/arduino-compiler/internal/config"
)

// TestCompileValidation tests that the compiler correctly validates inputs
// before attempting to run arduino-cli. These tests do NOT require
// arduino-cli to be installed.
func TestCompileValidation(t *testing.T) {
	cfg := &config.Config{
		MaxConcurrentCompilations: 2,
		CompileTimeout:            30 * time.Second,
		ArduinoCLIPath:            "arduino-cli",
	}
	comp := New(cfg)
	ctx := context.Background()

	t.Run("empty code returns validation error", func(t *testing.T) {
		_, err := comp.Compile(ctx, CompileRequest{
			Code:  "",
			Board: "arduino:avr:uno",
		})

		if err == nil {
			t.Fatal("expected error for empty code, got nil")
		}

		compErr, ok := err.(*CompileError)
		if !ok {
			t.Fatalf("expected *CompileError, got %T", err)
		}
		if compErr.Type != ErrValidation {
			t.Errorf("expected ErrValidation, got %v", compErr.Type)
		}
	})

	t.Run("whitespace-only code returns validation error", func(t *testing.T) {
		_, err := comp.Compile(ctx, CompileRequest{
			Code:  "   \n\t  ",
			Board: "arduino:avr:uno",
		})

		if err == nil {
			t.Fatal("expected error for whitespace-only code, got nil")
		}

		compErr, ok := err.(*CompileError)
		if !ok {
			t.Fatalf("expected *CompileError, got %T", err)
		}
		if compErr.Type != ErrValidation {
			t.Errorf("expected ErrValidation, got %v", compErr.Type)
		}
	})

	t.Run("unsupported board returns validation error", func(t *testing.T) {
		_, err := comp.Compile(ctx, CompileRequest{
			Code:  "void setup() {} void loop() {}",
			Board: "unsupported:board:fqbn",
		})

		if err == nil {
			t.Fatal("expected error for unsupported board, got nil")
		}

		compErr, ok := err.(*CompileError)
		if !ok {
			t.Fatalf("expected *CompileError, got %T", err)
		}
		if compErr.Type != ErrValidation {
			t.Errorf("expected ErrValidation, got %v", compErr.Type)
		}
	})

	t.Run("empty board returns validation error", func(t *testing.T) {
		_, err := comp.Compile(ctx, CompileRequest{
			Code:  "void setup() {} void loop() {}",
			Board: "",
		})

		if err == nil {
			t.Fatal("expected error for empty board, got nil")
		}

		compErr, ok := err.(*CompileError)
		if !ok {
			t.Fatalf("expected *CompileError, got %T", err)
		}
		if compErr.Type != ErrValidation {
			t.Errorf("expected ErrValidation, got %v", compErr.Type)
		}
	})
}

// TestSemaphoreCapacity tests that the compiler correctly rejects requests
// when the concurrency semaphore is full.
func TestSemaphoreCapacity(t *testing.T) {
	cfg := &config.Config{
		MaxConcurrentCompilations: 1, // Only allow 1 concurrent compilation.
		CompileTimeout:            5 * time.Second,
		ArduinoCLIPath:            "false", // "false" is a valid command that exits with code 1.
	}
	comp := New(cfg)

	// Fill the semaphore.
	comp.semaphore <- struct{}{}

	// Try to compile — should get a capacity error.
	_, err := comp.Compile(context.Background(), CompileRequest{
		Code:  "void setup() {} void loop() {}",
		Board: "arduino:avr:uno",
	})

	// Release the semaphore slot.
	<-comp.semaphore

	if err == nil {
		t.Fatal("expected capacity error, got nil")
	}

	compErr, ok := err.(*CompileError)
	if !ok {
		t.Fatalf("expected *CompileError, got %T", err)
	}
	if compErr.Type != ErrCapacity {
		t.Errorf("expected ErrCapacity, got %v", compErr.Type)
	}
}

// TestGetSupportedBoards tests that GetSupportedBoards returns a non-empty
// map and that modifications to the returned map don't affect the original.
func TestGetSupportedBoards(t *testing.T) {
	boards := GetSupportedBoards()

	if len(boards) == 0 {
		t.Fatal("expected non-empty supported boards map")
	}

	// Verify key boards are present.
	expectedBoards := []string{
		"arduino:avr:uno",
		"arduino:avr:mega",
		"arduino:avr:nano",
		"esp32:esp32:esp32",
		"esp8266:esp8266:generic",
	}

	for _, fqbn := range expectedBoards {
		if _, ok := boards[fqbn]; !ok {
			t.Errorf("expected board %q in supported boards", fqbn)
		}
	}

	// Verify the returned map is a copy (modifications don't affect the original).
	boards["test:test:test"] = "Test Board"
	original := GetSupportedBoards()
	if _, ok := original["test:test:test"]; ok {
		t.Error("modifying returned map should not affect the original")
	}
}

// TestCompileErrorInterface tests that CompileError properly implements
// the error interface.
func TestCompileErrorInterface(t *testing.T) {
	t.Run("error without details", func(t *testing.T) {
		err := &CompileError{
			Type:    ErrValidation,
			Message: "test error",
		}
		if err.Error() != "test error" {
			t.Errorf("expected %q, got %q", "test error", err.Error())
		}
	})

	t.Run("error with details", func(t *testing.T) {
		err := &CompileError{
			Type:    ErrCompilation,
			Message: "compilation failed",
			Details: "line 1: syntax error",
		}
		expected := "compilation failed: line 1: syntax error"
		if err.Error() != expected {
			t.Errorf("expected %q, got %q", expected, err.Error())
		}
	})
}
