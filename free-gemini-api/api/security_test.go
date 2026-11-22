package api

import (
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestSafeOutputPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.MkdirAll("output", 0755); err != nil {
		t.Fatal(err)
	}

	valid, ok := safeOutputPath("img_123.png")
	if !ok {
		t.Fatal("expected a simple filename to be accepted")
	}
	if !strings.HasSuffix(valid, filepath.Join("output", "img_123.png")) {
		t.Fatalf("unexpected resolved path: %s", valid)
	}

	cases := []string{
		"",
		".",
		"..",
		"../etc/passwd",
		"../../etc/passwd",
		"/etc/passwd",
		"a/../../b",
		"a\\..\\b",
		"..%2f..%2fetc%2fpasswd",
	}
	for _, name := range cases {
		if _, ok := safeOutputPath(name); ok {
			t.Errorf("safeOutputPath(%q) accepted a traversal candidate", name)
		}
	}
}

func TestSafeOutputPathRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.MkdirAll("output", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join("output", "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, ok := safeOutputPath("escape"); ok {
		t.Fatal("safeOutputPath accepted a symlink escaping ./output")
	}
}

func TestCORSRejectsHostileOrigin(t *testing.T) {
	app := fiber.New()
	app.Use(CORSStrictMiddleware())
	app.Post("/v1/models", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest("POST", "/v1/models", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("hostile cross-origin POST = %d, want 403", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected Access-Control-Allow-Origin: %q", got)
	}
}

func TestCORSAllowsLoopbackOrigin(t *testing.T) {
	app := fiber.New()
	app.Use(CORSStrictMiddleware())
	app.Get("/v1/models", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"object": "list"})
	})

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Origin", "http://127.0.0.1:8100")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("loopback origin = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:8100" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}
