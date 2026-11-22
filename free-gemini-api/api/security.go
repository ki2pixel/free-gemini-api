package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/gofiber/fiber/v3/middleware/limiter"
)

// SecurityHeadersMiddleware adds the baseline browser hardening headers. It is
// deliberately conservative: no CSP is injected here because the help page and
// the API have different content models.
func SecurityHeadersMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("X-Frame-Options", "DENY")
		c.Set("Referrer-Policy", "no-referrer")
		c.Set("X-XSS-Protection", "0")
		c.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		c.Set("Cross-Origin-Resource-Policy", "same-origin")
		return c.Next()
	}
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func defaultAllowedOrigins() []string {
	return []string{
		"http://127.0.0.1:8100",
		"http://localhost:8100",
	}
}

func configuredOrigins() (origins []string, strict bool) {
	raw := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS"))
	if raw == "" {
		return defaultAllowedOrigins(), false
	}
	for _, origin := range strings.Split(raw, ",") {
		origin = strings.TrimSpace(strings.TrimSuffix(origin, "/"))
		if origin != "" {
			origins = append(origins, origin)
		}
	}
	return origins, true
}

func originAllowed(origin string, origins []string, strict bool) bool {
	origin = strings.TrimSpace(strings.TrimSuffix(origin, "/"))
	if origin == "" || origin == "null" {
		return false
	}

	for _, allowed := range origins {
		if strings.EqualFold(origin, allowed) {
			return true
		}
	}

	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}

	// Chrome extensions are identified by a generated extension ID that is not
	// known at build time. Requiring API_TOKEN on every request keeps this from
	// becoming an open CORS policy.
	if parsed.Scheme == "chrome-extension" {
		return true
	}

	// Local dashboards are commonly served from a different loopback port.
	// Browsers cannot forge an Origin for a page the user did not load, so
	// allowing loopback origins is safe. An explicit CORS_ALLOWED_ORIGINS list
	// disables this convenience.
	if !strict && (parsed.Scheme == "http" || parsed.Scheme == "https") && isLoopbackHost(parsed.Hostname()) {
		return true
	}

	return false
}

// CORSStrictMiddleware replaces the previous wide-open CORS policy with an
// allowlist. Cross-origin mutations are rejected outright, while preflight
// requests are answered by Fiber's CORS middleware.
func CORSStrictMiddleware() fiber.Handler {
	origins, strict := configuredOrigins()

	corsHandler := cors.New(cors.Config{
		AllowOriginsFunc: func(origin string) bool {
			return originAllowed(origin, origins, strict)
		},
		AllowMethods: []string{
			fiber.MethodGet,
			fiber.MethodHead,
			fiber.MethodPost,
			fiber.MethodOptions,
		},
		AllowHeaders: []string{
			"Accept",
			"Authorization",
			"Content-Type",
			"Origin",
			"X-API-Token",
			"X-Agent-ID",
			"X-Session-ID",
			"X-Conversation-ID",
			"X-Gemini-Mode",
		},
		ExposeHeaders: []string{
			"Content-Disposition",
			"X-RateLimit-Limit",
			"X-RateLimit-Remaining",
			"X-RateLimit-Reset",
			"Retry-After",
		},
		AllowCredentials: false,
		MaxAge:           600,
	})

	return func(c fiber.Ctx) error {
		origin := c.Get(fiber.HeaderOrigin)
		if origin != "" && c.Method() != fiber.MethodGet && c.Method() != fiber.MethodHead && c.Method() != fiber.MethodOptions {
			if !originAllowed(origin, origins, strict) {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "origin not allowed"})
			}
		}
		return corsHandler(c)
	}
}

func rateLimitConfig() (int, time.Duration) {
	maxRequests := 600
	if raw := strings.TrimSpace(os.Getenv("RATE_LIMIT_MAX")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			maxRequests = parsed
		}
	}

	window := time.Minute
	if raw := strings.TrimSpace(os.Getenv("RATE_LIMIT_WINDOW_SECONDS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			window = time.Duration(parsed) * time.Second
		}
	}
	return maxRequests, window
}

// RateLimitMiddleware applies a fixed-window limit per client IP. The token, if
// present, is hashed into the key so separate clients behind one IP get
// independent budgets without storing the secret in limiter memory.
func RateLimitMiddleware() fiber.Handler {
	maxRequests, window := rateLimitConfig()

	return limiter.New(limiter.Config{
		Max:        maxRequests,
		Expiration: window,
		Next: func(c fiber.Ctx) bool {
			return c.Path() == "/health" || c.Method() == fiber.MethodOptions
		},
		KeyGenerator: func(c fiber.Ctx) string {
			token := extractAPIToken(c)
			if token == "" {
				return c.IP()
			}
			sum := sha256.Sum256([]byte(token))
			return c.IP() + ":" + hex.EncodeToString(sum[:8])
		},
		LimitReached: func(c fiber.Ctx) error {
			RecordRateLimitDrop()
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": "rate limit exceeded"})
		},
	})
}
