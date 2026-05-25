// Package server provides the HTTP server, routing, and middleware for the
// Arduino compiler API.
//
// Middleware stack (applied in order):
//  1. Recovery — catches panics and returns 500 instead of crashing.
//  2. Logging — logs method, path, status code, and duration.
//  3. CORS — handles cross-origin requests from browser clients.
//  4. RateLimit — per-IP token bucket rate limiting.
//  5. MaxBodySize — rejects oversized request bodies.
package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/derivative-cli/arduino-compiler/internal/config"
)

// recoveryMiddleware catches panics in downstream handlers, logs the error,
// and returns an HTTP 500 response instead of crashing the server.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[PANIC] %s %s — recovered from panic: %v", r.Method, r.URL.Path, err)
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code for logging.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

// WriteHeader captures the status code before delegating to the underlying writer.
func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

// Write captures a 200 status if WriteHeader hasn't been called yet.
func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.statusCode = http.StatusOK
		rw.written = true
	}
	return rw.ResponseWriter.Write(b)
}

// loggingMiddleware logs each request with its method, path, status code,
// duration, and client IP address.
func loggingMiddleware(trustProxy bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(wrapped, r)

			duration := time.Since(start)
			log.Printf("[HTTP] %s %s — %d (%s) from %s",
				r.Method,
				r.URL.Path,
				wrapped.statusCode,
				duration.Round(time.Millisecond),
				clientIP(r, trustProxy),
			)
		})
	}
}

// corsMiddleware handles Cross-Origin Resource Sharing (CORS) headers.
// It allows browser-based clients to make requests to the API.
//
// For preflight (OPTIONS) requests, it responds immediately with the
// appropriate headers. For all other requests, it sets the CORS headers
// and delegates to the next handler.
func corsMiddleware(allowedOrigins string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Determine if the origin is allowed.
			allowed := false
			if allowedOrigins == "*" {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				allowed = true
			} else if origin != "" {
				for _, ao := range strings.Split(allowedOrigins, ",") {
					if strings.TrimSpace(ao) == origin {
						w.Header().Set("Access-Control-Allow-Origin", origin)
						w.Header().Set("Vary", "Origin")
						allowed = true
						break
					}
				}
			}

			// Only set CORS method/header/expose headers if origin is allowed.
			if allowed {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
				w.Header().Set("Access-Control-Max-Age", "86400") // 24 hours
				// Expose custom response headers so browser JS can read them.
				// Without this, Content-Disposition, X-Board-Name, X-Filename,
				// X-Cache, and Content-Length are invisible to frontend JavaScript.
				w.Header().Set("Access-Control-Expose-Headers",
					"Content-Disposition, X-Board-Name, X-Filename, X-Cache, Content-Length")
			}

			// Handle preflight requests.
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ipLimiter stores per-IP rate limiters.
type ipLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rateLimiterEntry
	rate     rate.Limit
	burst    int
	cancel   context.CancelFunc // Stops the cleanup goroutine.
}

// rateLimiterEntry holds a limiter and the last time it was accessed,
// allowing stale entries to be cleaned up.
type rateLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// newIPLimiter creates a new per-IP rate limiter with the given requests-per-minute limit.
// The cleanup goroutine runs until Stop() is called.
func newIPLimiter(rpm int) *ipLimiter {
	ctx, cancel := context.WithCancel(context.Background())
	l := &ipLimiter{
		limiters: make(map[string]*rateLimiterEntry),
		rate:     rate.Limit(float64(rpm) / 60.0), // Convert RPM to per-second rate.
		burst:    rpm,                             // Allow bursts up to the RPM limit.
		cancel:   cancel,
	}

	// Start a background goroutine to clean up stale entries every 5 minutes.
	go l.cleanup(ctx)

	return l
}

// Stop cancels the cleanup goroutine. Safe to call multiple times.
func (l *ipLimiter) Stop() {
	l.cancel()
}

// getLimiter returns the rate limiter for the given IP, creating one if needed.
func (l *ipLimiter) getLimiter(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, exists := l.limiters[ip]
	if !exists {
		limiter := rate.NewLimiter(l.rate, l.burst)
		l.limiters[ip] = &rateLimiterEntry{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}

	entry.lastSeen = time.Now()
	return entry.limiter
}

// cleanup removes rate limiter entries that haven't been seen in 10 minutes.
// It stops when the context is cancelled.
func (l *ipLimiter) cleanup(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.mu.Lock()
			for ip, entry := range l.limiters {
				if time.Since(entry.lastSeen) > 10*time.Minute {
					delete(l.limiters, ip)
				}
			}
			l.mu.Unlock()
		}
	}
}

// rateLimitMiddleware enforces per-IP rate limiting. Requests that exceed the
// limit receive HTTP 429 Too Many Requests.
func rateLimitMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
	limiter := newIPLimiter(cfg.RateLimitRPM)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r, cfg.TrustProxy)
			if !limiter.getLimiter(ip).Allow() {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprintf(w, `{"error":"rate limit exceeded","message":"too many requests, please try again later"}`)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// maxBodySizeMiddleware limits the size of request bodies. Requests with bodies
// exceeding the configured maximum receive HTTP 413 Payload Too Large.
func maxBodySizeMiddleware(maxSize int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxSize {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				fmt.Fprintf(w, `{"error":"request too large","message":"request body exceeds maximum size of %d bytes"}`, maxSize)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxSize)
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP extracts the client's IP address from the request.
// When trustProxy is true, it checks X-Forwarded-For and X-Real-IP headers
// (for reverse proxy setups) before falling back to the remote address.
// When trustProxy is false, it only uses the remote address, preventing
// clients from spoofing their IP to bypass rate limiting.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		// Check X-Forwarded-For header (may contain multiple IPs: client, proxy1, proxy2).
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// The first IP is the original client.
			if idx := strings.Index(xff, ","); idx != -1 {
				return strings.TrimSpace(xff[:idx])
			}
			return strings.TrimSpace(xff)
		}

		// Check X-Real-IP header.
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	// Fall back to the remote address (strip port).
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
