package api

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func newAuthTestApp(apiToken, adminToken string) *fiber.App {
	app := fiber.New()
	app.Use(SecurityHeadersMiddleware())
	app.Use(AuthMiddleware(apiToken, adminToken))

	app.Get("/health", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "online"})
	})
	app.Get("/history", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"messages": []any{}})
	})
	app.Post("/v1/models", AdminAuthMiddleware(adminToken, apiToken), func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "success"})
	})
	return app
}

func doRequest(t *testing.T, app *fiber.App, method, path string, headers map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestAuthMiddlewareHealthIsPublic(t *testing.T) {
	app := newAuthTestApp("api-secret", "admin-secret")
	if status := doRequest(t, app, "GET", "/health", nil); status != 200 {
		t.Fatalf("GET /health = %d, want 200", status)
	}
}

func TestAuthMiddlewareRejectsMissingAndInvalidToken(t *testing.T) {
	app := newAuthTestApp("api-secret", "admin-secret")

	if status := doRequest(t, app, "GET", "/history", nil); status != 401 {
		t.Fatalf("GET /history without token = %d, want 401", status)
	}
	if status := doRequest(t, app, "GET", "/history", map[string]string{"X-API-Token": "wrong"}); status != 401 {
		t.Fatalf("GET /history with wrong token = %d, want 401", status)
	}
}

func TestAuthMiddlewareAcceptsBearerAndHeader(t *testing.T) {
	app := newAuthTestApp("api-secret", "admin-secret")

	if status := doRequest(t, app, "GET", "/history", map[string]string{"Authorization": "Bearer api-secret"}); status != 200 {
		t.Fatalf("Bearer token = %d, want 200", status)
	}
	if status := doRequest(t, app, "GET", "/history", map[string]string{"X-API-Token": "api-secret"}); status != 200 {
		t.Fatalf("X-API-Token = %d, want 200", status)
	}
	if status := doRequest(t, app, "GET", "/history", map[string]string{"Authorization": "bearer api-secret"}); status != 200 {
		t.Fatalf("lowercase bearer = %d, want 200", status)
	}
}

func TestAuthMiddlewareEmptyTokenNeverOpensServer(t *testing.T) {
	app := newAuthTestApp("", "")
	if status := doRequest(t, app, "GET", "/history", nil); status != 401 {
		t.Fatalf("empty configured token = %d, want 401", status)
	}
	if status := doRequest(t, app, "GET", "/history", map[string]string{"X-API-Token": "anything"}); status != 401 {
		t.Fatalf("empty configured token with supplied value = %d, want 401", status)
	}
}

func TestAdminAuthRequiresAdminTokenWhenConfigured(t *testing.T) {
	app := newAuthTestApp("api-secret", "admin-secret")

	if status := doRequest(t, app, "POST", "/v1/models", map[string]string{"X-API-Token": "api-secret"}); status != 401 {
		t.Fatalf("admin route with API token = %d, want 401", status)
	}
	if status := doRequest(t, app, "POST", "/v1/models", map[string]string{"X-API-Token": "admin-secret"}); status != 200 {
		t.Fatalf("admin route with admin token = %d, want 200", status)
	}
}

func TestAdminAuthFallsBackToAPIToken(t *testing.T) {
	app := newAuthTestApp("api-secret", "")
	if status := doRequest(t, app, "POST", "/v1/models", map[string]string{"X-API-Token": "api-secret"}); status != 200 {
		t.Fatalf("admin route with fallback API token = %d, want 200", status)
	}
}

func TestPublicHelpToggle(t *testing.T) {
	t.Setenv("PUBLIC_HELP", "true")

	app := fiber.New()
	app.Use(AuthMiddleware("api-secret"))
	app.Get("/", func(c fiber.Ctx) error { return c.SendString("root") })
	app.Get("/help", func(c fiber.Ctx) error { return c.SendString("help") })
	app.Get("/history", func(c fiber.Ctx) error { return c.SendString("history") })

	if status := doRequest(t, app, "GET", "/help", nil); status != 200 {
		t.Fatalf("PUBLIC_HELP=true /help without token = %d, want 200", status)
	}
	if status := doRequest(t, app, "GET", "/", nil); status != 200 {
		t.Fatalf("PUBLIC_HELP=true / without token = %d, want 200", status)
	}
	if status := doRequest(t, app, "GET", "/history", nil); status != 401 {
		t.Fatalf("PUBLIC_HELP=true /history without token = %d, want 401", status)
	}
}
