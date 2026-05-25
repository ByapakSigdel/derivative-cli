// Package config provides environment-based configuration for the Arduino compiler server.
//
// All configuration values can be set via environment variables. If an environment variable
// is not set, a sensible default value is used. This makes the server easy to configure
// in Docker containers without modifying code.
package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all server configuration values.
type Config struct {
	// Port is the TCP port the HTTP server listens on.
	// Environment variable: PORT
	// Default: 8080
	Port string

	// MaxConcurrentCompilations is the maximum number of compilations that can
	// run simultaneously. Additional requests will receive HTTP 429 (Too Many Requests).
	// Environment variable: MAX_CONCURRENT_COMPILATIONS
	// Default: 5
	MaxConcurrentCompilations int

	// CompileTimeout is the maximum duration allowed for a single compilation.
	// If a compilation exceeds this duration, the process is killed and the
	// request returns an error.
	// Environment variable: COMPILE_TIMEOUT (in seconds)
	// Default: 120 seconds
	CompileTimeout time.Duration

	// RateLimitRPM is the maximum number of requests per minute allowed from
	// a single IP address. Requests exceeding this limit receive HTTP 429.
	// Environment variable: RATE_LIMIT_RPM
	// Default: 10
	RateLimitRPM int

	// MaxRequestSize is the maximum allowed size of a request body in bytes.
	// Requests with bodies larger than this are rejected with HTTP 413.
	// Environment variable: MAX_REQUEST_SIZE
	// Default: 1048576 (1 MB)
	MaxRequestSize int64

	// ArduinoCLIPath is the filesystem path to the arduino-cli binary.
	// Environment variable: ARDUINO_CLI_PATH
	// Default: arduino-cli (resolved via PATH)
	ArduinoCLIPath string

	// AllowedOrigins is a comma-separated list of allowed CORS origins.
	// Use "*" to allow all origins.
	// Environment variable: ALLOWED_ORIGINS
	// Default: * (all origins)
	AllowedOrigins string

	// TrustProxy controls whether X-Forwarded-For and X-Real-IP headers are
	// trusted for determining the client's IP address. Set to true when the
	// server runs behind a reverse proxy (nginx, Cloudflare, etc.). When false,
	// only the direct TCP connection address is used, preventing clients from
	// spoofing their IP to bypass rate limiting.
	// Environment variable: TRUST_PROXY
	// Default: false
	TrustProxy bool

	// RedisURL is the connection URL for the compile cache (e.g.
	// "redis://redis:6379"). When empty, caching is disabled and every request
	// is compiled from scratch.
	// Environment variable: REDIS_URL
	// Default: "" (caching disabled)
	RedisURL string

	// CacheTTL is how long a compiled binary stays in the cache before
	// expiring. LRU eviction may remove it sooner under memory pressure.
	// Environment variable: CACHE_TTL (in seconds)
	// Default: 1209600 (14 days)
	CacheTTL time.Duration

	// CacheVersion namespaces cache keys. A cached binary is only valid for the
	// exact toolchain that produced it, so BUMP THIS (e.g. "v1" -> "v2")
	// whenever you re-provision arduino-cli cores or libraries — old entries
	// then expire on their own instead of being served stale.
	// Environment variable: CACHE_VERSION
	// Default: "v1"
	CacheVersion string
}

// Load reads configuration from environment variables and returns a Config
// with all values populated. Missing environment variables fall back to defaults.
// Invalid values are clamped to safe minimums to prevent runtime panics.
func Load() *Config {
	maxConcurrent := getEnvInt("MAX_CONCURRENT_COMPILATIONS", 5)
	if maxConcurrent < 1 {
		log.Printf("[WARN] MAX_CONCURRENT_COMPILATIONS=%d is invalid, using minimum of 1", maxConcurrent)
		maxConcurrent = 1
	}

	rateLimitRPM := getEnvInt("RATE_LIMIT_RPM", 10)
	if rateLimitRPM < 1 {
		log.Printf("[WARN] RATE_LIMIT_RPM=%d is invalid, using minimum of 1", rateLimitRPM)
		rateLimitRPM = 1
	}

	maxRequestSize := getEnvInt64("MAX_REQUEST_SIZE", 1048576)
	if maxRequestSize < 1024 {
		log.Printf("[WARN] MAX_REQUEST_SIZE=%d is too small, using minimum of 1024 bytes", maxRequestSize)
		maxRequestSize = 1024
	}

	compileTimeout := getEnvInt("COMPILE_TIMEOUT", 120)
	if compileTimeout < 10 {
		log.Printf("[WARN] COMPILE_TIMEOUT=%d is too short, using minimum of 10 seconds", compileTimeout)
		compileTimeout = 10
	}

	cacheTTL := getEnvInt("CACHE_TTL", 1209600) // 14 days.
	if cacheTTL < 60 {
		log.Printf("[WARN] CACHE_TTL=%d is too short, using minimum of 60 seconds", cacheTTL)
		cacheTTL = 60
	}

	return &Config{
		Port:                      getEnv("PORT", "8080"),
		MaxConcurrentCompilations: maxConcurrent,
		CompileTimeout:            time.Duration(compileTimeout) * time.Second,
		RateLimitRPM:              rateLimitRPM,
		MaxRequestSize:            maxRequestSize,
		ArduinoCLIPath:            getEnv("ARDUINO_CLI_PATH", "arduino-cli"),
		AllowedOrigins:            getEnv("ALLOWED_ORIGINS", "*"),
		TrustProxy:                getEnvBool("TRUST_PROXY", false),
		RedisURL:                  getEnv("REDIS_URL", ""),
		CacheTTL:                  time.Duration(cacheTTL) * time.Second,
		CacheVersion:              getEnv("CACHE_VERSION", "v1"),
	}
}

// getEnv returns the value of the environment variable named by key,
// or defaultVal if the variable is not set or is empty.
func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// getEnvInt returns the integer value of the environment variable named by key,
// or defaultVal if the variable is not set, is empty, or cannot be parsed as an integer.
func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			return parsed
		}
	}
	return defaultVal
}

// getEnvInt64 returns the int64 value of the environment variable named by key,
// or defaultVal if the variable is not set, is empty, or cannot be parsed.
func getEnvInt64(key string, defaultVal int64) int64 {
	if val := os.Getenv(key); val != "" {
		if parsed, err := strconv.ParseInt(val, 10, 64); err == nil {
			return parsed
		}
	}
	return defaultVal
}

// getEnvBool returns the boolean value of the environment variable named by key,
// or defaultVal if the variable is not set, is empty, or cannot be parsed.
// Accepted true values: "true", "1", "yes", "on" (case-insensitive).
// Everything else is treated as false.
func getEnvBool(key string, defaultVal bool) bool {
	if val := os.Getenv(key); val != "" {
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	}
	return defaultVal
}
