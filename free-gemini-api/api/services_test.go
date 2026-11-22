package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"goapi/db"
	"goapi/gemini"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("empty = %d, want 0", got)
	}
	if got := EstimateTokens("hello"); got < 1 {
		t.Fatalf("hello = %d, want >= 1", got)
	}
	long := strings.Repeat("word ", 1000)
	if got := EstimateTokens(long); got < 1000 {
		t.Fatalf("long text = %d, want >= 1000", got)
	}
}

func TestSanitizeSessionID(t *testing.T) {
	cases := []struct {
		raw, fallback, want string
	}{
		{"valid_id-1", "127.0.0.1", "valid_id-1"},
		{"", "127.0.0.1", "127.0.0.1"},
		{"", "", "anonymous"},
	}
	for _, tc := range cases {
		if got := sanitizeSessionID(tc.raw, tc.fallback); got != tc.want {
			t.Errorf("sanitizeSessionID(%q,%q) = %q, want %q", tc.raw, tc.fallback, got, tc.want)
		}
	}

	got := sanitizeSessionID("bad id with spaces", "fallback")
	if !strings.HasPrefix(got, "s_") {
		t.Fatalf("invalid id should be hashed, got %q", got)
	}
	if again := sanitizeSessionID("bad id with spaces", "fallback"); again != got {
		t.Fatal("hashing must be deterministic")
	}
	if strings.Contains(got, " ") {
		t.Fatal("hashed id must not contain spaces")
	}
}

func TestChatServiceHelpers(t *testing.T) {
	svc := GetChatService()
	if svc == nil || svc.Pool() == nil || svc.History() == nil {
		t.Fatal("chat service dependencies must be initialized")
	}
	if StreamTimeout() != 15*time.Minute {
		t.Fatalf("unexpected stream timeout %v", StreamTimeout())
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	CancelWhenRequestDone(done, ctx, cancel)
	close(done)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel was not propagated")
	}

	// A nil done channel must be a no-op.
	ctx2, cancel2 := context.WithCancel(context.Background())
	CancelWhenRequestDone(nil, ctx2, cancel2)
	cancel2()
}

func TestDecodeUnifiedChatJSON(t *testing.T) {
	app := fiber.New()
	app.Post("/decode", func(c fiber.Ctx) error {
		input, err := GetChatService().DecodeUnifiedChat(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(input)
	})

	body := `{"prompt":"hello","user_id":"user-1","model":"gemini-3.8-flash","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/decode", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// message fallback
	req = httptest.NewRequest(http.MethodPost, "/decode", strings.NewReader(`{"message":"fallback"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ = app.Test(req)
	if resp.StatusCode != 200 {
		t.Fatalf("message fallback status = %d", resp.StatusCode)
	}

	// invalid JSON
	req = httptest.NewRequest(http.MethodPost, "/decode", strings.NewReader(`{`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ = app.Test(req)
	if resp.StatusCode != 400 {
		t.Fatalf("invalid JSON status = %d, want 400", resp.StatusCode)
	}

	// oversized prompt
	huge := fmt.Sprintf(`{"prompt":%q}`, strings.Repeat("a", maxPromptBytes+1))
	req = httptest.NewRequest(http.MethodPost, "/decode", strings.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	resp, _ = app.Test(req)
	if resp.StatusCode != 400 {
		t.Fatalf("oversized prompt status = %d, want 400", resp.StatusCode)
	}
}

func TestDecodeUnifiedChatMultipart(t *testing.T) {
	app := fiber.New()
	app.Post("/decode", func(c fiber.Ctx) error {
		input, err := GetChatService().DecodeUnifiedChat(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"prompt": input.Prompt, "images": len(input.Images), "mime": func() string {
			if len(input.Images) > 0 {
				return input.Images[0].MimeType
			}
			return ""
		}()})
	})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("prompt", "describe this")
	_ = writer.WriteField("user_id", "multipart-user")
	part, err := writer.CreateFormFile("images", "photo.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/decode", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// Broken multipart body. fasthttp may reject the request before the
	// handler runs; both an error and a 400 are acceptable rejections.
	req = httptest.NewRequest(http.MethodPost, "/decode", strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	resp, err = app.Test(req)
	if err == nil && resp != nil && resp.StatusCode != 400 {
		t.Fatalf("broken multipart status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadMimeType(t *testing.T) {
	cases := map[string]string{
		"a.png":  "image/png",
		"a.webp": "image/webp",
		"a.gif":  "image/gif",
		"a.mp4":  "video/mp4",
		"a.jpg":  "image/jpeg",
		"a.bin":  "image/jpeg",
	}
	for name, want := range cases {
		fh := &multipart.FileHeader{Filename: name}
		if got := uploadMimeType(fh); got != want {
			t.Errorf("uploadMimeType(%q) = %q, want %q", name, got, want)
		}
	}

	fh := &multipart.FileHeader{Filename: "a.png", Header: map[string][]string{"Content-Type": {"image/custom"}}}
	if got := uploadMimeType(fh); got != "image/custom" {
		t.Errorf("explicit content type = %q", got)
	}
}

func TestResolveModelAndSessionID(t *testing.T) {
	app := fiber.New()
	app.Post("/resolve", func(c fiber.Ctx) error {
		cfg, model, tier := GetChatService().ResolveModel(c, c.Query("model"))
		return c.JSON(fiber.Map{
			"id":       cfg.ID,
			"app_mode": cfg.AppMode,
			"model":    model,
			"tier":     tier,
			"session":  GetChatService().SessionID(c, c.Query("user")),
		})
	})

	req := httptest.NewRequest(http.MethodPost, "/resolve?model=gemini-3.8-flash-task&user=bad%20id", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if payload["app_mode"].(float64) != 40 {
		t.Errorf("task mode not applied: %v", payload)
	}
	if !strings.HasPrefix(payload["session"].(string), "s_") {
		t.Errorf("invalid session id not sanitized: %v", payload["session"])
	}
}

func TestHistoryServiceWorkflow(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Chdir(t.TempDir())

	svc := NewHistoryService(16)
	conv := "conv_history_service"
	svc.LogTurn(conv, "user-h", "prompt", "response", "model-h", 10, 20, "free", "acc-h")
	svc.LogMedia("image", conv, "prompt", "file.png", "./output/file.png", "http://x/file.png", "16:9", "resp-h")
	svc.LogRequest("/test", "GET", "127.0.0.1", 200, 5)
	svc.FlushAndClose()
	svc.FlushAndClose()

	messages, err := db.GetRecentMessages(conv, 10)
	if err != nil {
		t.Fatalf("GetRecentMessages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	// Sending after close must be dropped, not panic.
	svc.LogMessage(conv, "user-h", "user", "late", "model-h", 1, "free", "")
	if svc.Dropped() == 0 {
		t.Fatal("expected dropped counter to increase after close")
	}
}

func TestAnalyticsExportsAndHandlers(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Chdir(t.TempDir())
	_ = db.LogMessageWithTier("conv_export", "u", "user", "hello export", "m", 10, "free", "a")
	_ = db.LogMediaGeneration("image", "export prompt", "e.png", "./output/e.png", "http://x/e.png", "16:9", "r")
	_ = db.RecordAccountUsage("export-acc", "cookies/e.json", "active", "free")

	svc := DefaultAnalyticsService
	if _, err := svc.BuildJSON(); err != nil {
		t.Fatalf("BuildJSON: %v", err)
	}
	workbook, err := svc.BuildExcel()
	if err != nil {
		t.Fatalf("BuildExcel: %v", err)
	}
	_ = workbook.Close()

	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "analytics.json")
	if _, data, err := svc.ExportJSON(jsonPath); err != nil || len(data) == 0 {
		t.Fatalf("ExportJSON: %v", err)
	}
	excelPath := filepath.Join(dir, "analytics.xlsx")
	if _, err := svc.ExportExcel(excelPath); err != nil {
		t.Fatalf("ExportExcel: %v", err)
	}
	if info, err := os.Stat(excelPath); err != nil || info.Size() == 0 {
		t.Fatalf("excel file missing: %v", err)
	}

	// Empty path defaults exercise the default output branch.
	if _, _, err := ExportAnalyticsToJSON(""); err != nil {
		t.Fatalf("ExportAnalyticsToJSON default: %v", err)
	}
	if _, err := ExportAnalyticsToExcel(""); err != nil {
		t.Fatalf("ExportAnalyticsToExcel default: %v", err)
	}

	if err := atomicWriteFile(filepath.Join(dir, "atomic.txt"), []byte("data"), 0600); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}

	app := fiber.New()
	app.Get("/export/json", HandleJSONExport)
	app.Get("/export/excel", HandleExcelExport)
	for _, path := range []string{"/export/json", "/export/excel"} {
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("%s status = %d", path, resp.StatusCode)
		}
	}
}

func TestExportRateLimiter(t *testing.T) {
	limiter := newFixedWindowLimiter(2, time.Minute)
	for i := 0; i < 2; i++ {
		allowed, remaining, _ := limiter.allow("k")
		if !allowed || remaining != 1-i {
			t.Fatalf("call %d: allowed=%v remaining=%d", i, allowed, remaining)
		}
	}
	allowed, _, retry := limiter.allow("k")
	if allowed || retry <= 0 {
		t.Fatalf("third call should be limited, got allowed=%v retry=%v", allowed, retry)
	}

	app := fiber.New()
	app.Get("/export", ExportRateLimitMiddleware(), func(c fiber.Ctx) error {
		return c.SendString("ok")
	})
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodGet, "/export", nil)
		req.Header.Set("X-API-Token", "token-a")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if i < 5 && resp.StatusCode != 200 {
			t.Fatalf("request %d status = %d", i, resp.StatusCode)
		}
		if i == 5 && resp.StatusCode != 429 {
			t.Fatalf("request %d status = %d, want 429", i, resp.StatusCode)
		}
	}
}

func TestHelpAndSecurityMiddleware(t *testing.T) {
	app := fiber.New()
	app.Use(SecurityHeadersMiddleware())
	app.Get("/help", HandleHelp)
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/help", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("help status = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("security header missing")
	}
	if !strings.Contains(GetCLIHelpText(), "--mcp") {
		t.Error("CLI help should document --mcp")
	}

	t.Setenv("RATE_LIMIT_MAX", "2")
	t.Setenv("RATE_LIMIT_WINDOW_SECONDS", "60")
	maxReq, window := rateLimitConfig()
	if maxReq != 2 || window != time.Minute {
		t.Fatalf("rateLimitConfig = %d/%v", maxReq, window)
	}
}

func TestWorkerPoolAffinityAndStreaming(t *testing.T) {
	pool := createMockPool(2, 2)
	ctx := context.Background()

	pool.BindAffinity("affinity-key", "account_1")
	worker, err := pool.AcquireWorker(ctx, "affinity-key")
	if err != nil {
		t.Fatalf("AcquireWorker: %v", err)
	}
	if worker.ID != "account_1" {
		t.Fatalf("affinity selected %s, want account_1", worker.ID)
	}
	pool.ReleaseWorker(worker, nil)

	if err := pool.ExecuteQueuedStream(ctx, "stream-key", func(client *gemini.GeminiClient) error {
		return nil
	}); err != nil {
		t.Fatalf("ExecuteQueuedStream: %v", err)
	}

	if err := pool.ExecuteQueuedStreamWithTier(ctx, "stream-key-2", "free", func(worker *AccountWorker) error {
		return nil
	}); err != nil {
		t.Fatalf("ExecuteQueuedStreamWithTier: %v", err)
	}

	pool.mu.Lock()
	for _, worker := range pool.workers {
		worker.Client = nil
	}
	pool.mu.Unlock()

	result, err := pool.DiscoverModelsFromWorkers()
	if err != nil {
		t.Fatalf("DiscoverModelsFromWorkers: %v", err)
	}
	if result["workers_checked"] != 2 {
		t.Fatalf("workers_checked = %v, want 2", result["workers_checked"])
	}
}

func TestSessionHelpers(t *testing.T) {
	if err := DownloadAndClean(nil, "http://example.com/x", "../escape.png", "image"); err == nil {
		t.Fatal("invalid filename should be rejected before any network access")
	}
	if err := DownloadAndClean(nil, "http://example.com/x", "sub/dir.png", "image"); err == nil {
		t.Fatal("path components should be rejected")
	}

	client, err := GetOrCreateClientWithFile("sanitize-test", "guest")
	if err != nil {
		t.Fatalf("GetOrCreateClientWithFile: %v", err)
	}
	if client == nil {
		t.Fatal("expected guest client")
	}
	UserSessions.Delete("sanitize-test_guest")
}

func TestHandlerValidationPaths(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	app := fiber.New()
	app.Post("/chat", HandleUnifiedChat)
	app.Post("/v1/chat/completions", HandleOpenAIChatCompletions)
	app.Post("/music", HandleMusic)

	// Invalid JSON on all three handlers.
	for _, path := range []string{"/chat", "/v1/chat/completions", "/music"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 400 {
			t.Errorf("%s status = %d, want 400", path, resp.StatusCode)
		}
	}

	// Empty OpenAI messages.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-3.8-flash","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 400 {
		t.Errorf("empty messages status = %d, want 400", resp.StatusCode)
	}

	// Oversized prompts.
	huge := fmt.Sprintf(`{"prompt":%q}`, strings.Repeat("a", maxPromptBytes+1))
	for _, path := range []string{"/chat", "/music"} {
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(huge))
		req.Header.Set("Content-Type", "application/json")
		resp, _ = app.Test(req)
		if resp.StatusCode != fiber.StatusRequestEntityTooLarge {
			t.Errorf("%s oversized status = %d, want 413", path, resp.StatusCode)
		}
	}

	// Invalid multipart form on /chat. fasthttp may reject the request before
	// the handler runs; both an error and a 400 are acceptable rejections.
	req = httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader("broken"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	resp, testErr := app.Test(req)
	if testErr == nil && resp != nil && resp.StatusCode != 400 {
		t.Errorf("broken multipart status = %d, want 400", resp.StatusCode)
	}
}

func TestRegisterRoutesHealth(t *testing.T) {
	t.Setenv("API_TOKEN", "route-token")
	app := fiber.New()
	RegisterRoutes(app)
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/health", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("health status = %d", resp.StatusCode)
	}
}

func TestMCPHelpersAndBackendCalls(t *testing.T) {
	t.Setenv("GEMINI_API_URL", "http://backend.example")
	if got := getMCPBackendURL(); got != "http://backend.example" {
		t.Fatalf("backend URL = %q", got)
	}
	t.Setenv("GEMINI_API_URL", "")
	t.Setenv("PORT", "9999")
	if got := getMCPBackendURL(); got != "http://127.0.0.1:9999" {
		t.Fatalf("backend URL = %q", got)
	}

	t.Setenv("MCP_TOKEN", "mcp-secret")
	if got := mcpAuthToken(); got != "mcp-secret" {
		t.Fatalf("mcpAuthToken = %q", got)
	}
	t.Setenv("MCP_TOKEN", "")
	t.Setenv("API_TOKEN", "api-secret")
	if got := mcpAuthToken(); got != "api-secret" {
		t.Fatalf("mcpAuthToken fallback = %q", got)
	}

	req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
	applyMCPAuth(req)
	if req.Header.Get("Authorization") != "Bearer api-secret" {
		t.Fatalf("authorization header = %q", req.Header.Get("Authorization"))
	}

	defs := getMCPToolDefinitions()
	tools, ok := defs["tools"].([]map[string]interface{})
	if !ok || len(tools) != 6 {
		t.Fatalf("unexpected tool definitions: %v", defs)
	}

	if got := formatMCPToolError(fmt.Errorf("boom")); !strings.Contains(got, "boom") {
		t.Fatalf("formatMCPToolError = %q", got)
	}
}

func TestMCPBackendHandlers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	})
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok","images":["http://img/1.png"],"videos":["http://vid/1.mp4"]}`))
	})
	mux.HandleFunc("/music", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok","music":[{"local_path":"/x/a.mp3","download_url":"http://music/a.mp3","title":"Track"}]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	res, err := handleMCPHealthCheck(server.URL)
	if err != nil || res.IsError {
		t.Fatalf("health check: %v %+v", err, res)
	}
	res, err = handleMCPResetSession(server.URL)
	if err != nil || res.IsError {
		t.Fatalf("reset session: %v %+v", err, res)
	}

	media, err := handleMCPMediaGeneration("generate_image", PromptArg{Prompt: "cat"}, server.URL)
	if err != nil {
		t.Fatalf("media generation: %v", err)
	}
	if !strings.Contains(media.Content[0].Text, "img") {
		t.Fatalf("unexpected media response: %+v", media)
	}

	music, err := handleMCPMusicGeneration(PromptArg{Prompt: "beat"}, server.URL)
	if err != nil {
		t.Fatalf("music generation: %v", err)
	}
	if !strings.Contains(music.Content[0].Text, "Track") {
		t.Fatalf("unexpected music response: %+v", music)
	}

	chat, err := handleMCPChat(PromptArg{Prompt: "hello"}, server.URL)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !strings.Contains(chat.Content[0].Text, "ok") {
		t.Fatalf("unexpected chat response: %+v", chat)
	}

	if _, err := callMCPTool("unknown", PromptArg{Prompt: "x"}, server.URL); err == nil {
		t.Fatal("unknown tool should fail")
	}
	if _, err := callMCPTool("check_health", PromptArg{}, server.URL); err != nil {
		t.Fatalf("check_health through callMCPTool: %v", err)
	}
	if _, err := callMCPTool("reset_session", PromptArg{}, server.URL); err != nil {
		t.Fatalf("reset_session through callMCPTool: %v", err)
	}
}

func TestMCPMediaPathIntegration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MCP_MEDIA_ROOT", root)
	writeTestPNG(t, root, "ref.png")

	var sawMultipart bool
	mux := http.NewServeMux()
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			sawMultipart = true
			_ = r.ParseMultipartForm(1 << 20)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok","videos":["http://vid/1.mp4"]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	res, err := handleMCPMediaGeneration("generate_video", PromptArg{Prompt: "animate", StartImagePath: "ref.png"}, server.URL)
	if err != nil {
		t.Fatalf("media generation with reference: %v", err)
	}
	if !res.IsError && !strings.Contains(res.Content[0].Text, "vid") {
		t.Fatalf("unexpected response: %+v", res)
	}
	if !sawMultipart {
		t.Fatal("expected a multipart upload for the reference image")
	}

	// Path outside the media root must be rejected before any HTTP call.
	if _, err := handleMCPMediaGeneration("generate_image", PromptArg{Prompt: "x", RefImagePath: "/etc/passwd"}, server.URL); err == nil {
		t.Fatal("absolute path should be rejected")
	}
}

func TestMCPPostJSONAndMultipart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	t.Setenv("API_TOKEN", "token")

	resp, err := mcpPostJSON(server.URL, map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("mcpPostJSON: %v", err)
	}
	resp.Body.Close()

	root := t.TempDir()
	file := writeTestPNG(t, root, "file.png")
	resp, err = postMCPMultipart(server.URL, "prompt", "user", true, file)
	if err != nil {
		t.Fatalf("postMCPMultipart: %v", err)
	}
	resp.Body.Close()
}
