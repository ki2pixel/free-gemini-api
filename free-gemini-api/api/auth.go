package api

import (
	"crypto/subtle"
	"log"
	"os"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// extractAPIToken reads the caller token from either of the two supported
// headers. The Bearer scheme is matched case-insensitively so ordinary HTTP
// clients and OpenAI SDKs both work.
func extractAPIToken(c fiber.Ctx) string {
	auth := strings.TrimSpace(c.Get(fiber.HeaderAuthorization))
	if len(auth) > len("Bearer ") && strings.EqualFold(auth[:len("Bearer ")], "Bearer ") {
		if token := strings.TrimSpace(auth[len("Bearer "):]); token != "" {
			return token
		}
	}
	return strings.TrimSpace(c.Get("X-API-Token"))
}

// tokenMatches performs a constant-time comparison and never accepts an empty
// configured token or an empty supplied token.
func tokenMatches(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// anyTokenMatches checks every configured token without short-circuiting, so
// the comparison cost does not reveal which token matched.
func anyTokenMatches(got string, tokens ...string) bool {
	if got == "" {
		return false
	}
	matched := false
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
			matched = true
		}
	}
	return matched
}

// AuthMiddleware protects every route except /health and CORS preflight
// requests. An empty API_TOKEN intentionally rejects every protected request:
// a server started without a token must not silently become open.
//
// Extra tokens (for example ADMIN_TOKEN) are accepted by the global gate as
// well; the dedicated admin middleware still enforces the stronger token on
// sensitive routes.
func AuthMiddleware(apiToken string, extraTokens ...string) fiber.Handler {
	publicHelp := strings.EqualFold(strings.TrimSpace(os.Getenv("PUBLIC_HELP")), "true")

	return func(c fiber.Ctx) error {
		path := c.Path()
		if path == "/health" || c.Method() == fiber.MethodOptions {
			return c.Next()
		}
		if publicHelp && (path == "/" || path == "/help") {
			return c.Next()
		}
		if path == "/metrics" && publicMetricsRequestAllowed(c) {
			return c.Next()
		}

		if !anyTokenMatches(extractAPIToken(c), append([]string{apiToken}, extraTokens...)...) {
			log.Printf("🔒 auth denied: ip=%s method=%s path=%s", c.IP(), c.Method(), path)
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		return c.Next()
	}
}

// AdminAuthMiddleware guards state-changing administrative endpoints. When
// ADMIN_TOKEN is set it is the only accepted credential; otherwise the regular
// API token is used as a fallback so a single-token local setup keeps working.
func AdminAuthMiddleware(adminToken, apiToken string) fiber.Handler {
	token := strings.TrimSpace(adminToken)
	if token == "" {
		token = strings.TrimSpace(apiToken)
	}

	return func(c fiber.Ctx) error {
		if c.Method() == fiber.MethodOptions {
			return c.Next()
		}
		if !tokenMatches(extractAPIToken(c), token) {
			log.Printf("🔒 admin auth denied: ip=%s method=%s path=%s", c.IP(), c.Method(), c.Path())
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		return c.Next()
	}
}
