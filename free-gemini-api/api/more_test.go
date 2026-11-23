package api

import (
	"encoding/json"
	"goapi/db"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

func TestTokenizer(t *testing.T) {
	if got := CountTokens("hello world"); got < 1 {
		t.Fatalf("CountTokens = %d, want >= 1", got)
	}
	if got := SafeCountTokens("hello world"); got < 1 {
		t.Fatalf("SafeCountTokens = %d, want >= 1", got)
	}
	if got := CountTokensLimited("hello world"); got < 1 {
		t.Fatalf("CountTokensLimited = %d, want >= 1", got)
	}
	large := strings.Repeat("a", maxTokenizeBytes+1)
	if got := SafeCountTokens(large); got < 1 {
		t.Fatalf("large SafeCountTokens = %d, want >= 1", got)
	}
}

func TestSessionTranscriptPlanning(t *testing.T) {
	key := "transcript-test-" + t.Name()
	flat := "User: hello\n"
	commitSessionTurn(key, flat, "conv-1", "resp-1", "choice-1", "worker-1")

	conv, resp, choice, worker, valid := peekSessionTurnConv(key)
	if conv != "conv-1" || resp != "resp-1" || choice != "choice-1" || worker != "worker-1" || !valid {
		t.Fatalf("unexpected peek: %q %q %q %q %v", conv, resp, choice, worker, valid)
	}

	// No live conversation -> full replay.
	if prompt, reset := planSessionTurn(key, "", flat+"x"); !reset || prompt != flat+"x" {
		t.Fatalf("empty conversation should reset: %q %v", prompt, reset)
	}
	// Identical transcript -> full replay (retry).
	if prompt, reset := planSessionTurn(key, "conv-1", flat); !reset || prompt != flat {
		t.Fatalf("identical transcript should reset: %q %v", prompt, reset)
	}
	// Strict extension -> delta.
	if prompt, reset := planSessionTurn(key, "conv-1", flat+"Assistant: hi\n"); reset || prompt != "Assistant: hi\n" {
		t.Fatalf("extension should produce a delta: %q %v", prompt, reset)
	}
	// Divergent transcript -> full replay.
	if prompt, reset := planSessionTurn(key, "conv-1", "different"); !reset || prompt != "different" {
		t.Fatalf("divergence should reset: %q %v", prompt, reset)
	}

	invalidateSessionTurn(key)
	if _, _, _, _, valid := peekSessionTurnConv(key); valid {
		t.Fatal("invalidated turn should report convValid=false")
	}
	if _, reset := planSessionTurn(key, "conv-1", flat+"more"); !reset {
		t.Fatal("invalidated conversation should reset")
	}

	if _, reset := planSessionTurn("unknown-session", "conv", "flat"); !reset {
		t.Fatal("unknown session should reset")
	}
}

func TestScopeKeysAndOutputClassification(t *testing.T) {
	firstScope := systemScopeKey("abc")
	if firstScope != systemScopeKey("abc") {
		t.Fatal("systemScopeKey must be deterministic")
	}
	if systemScopeKey("abc") == systemScopeKey("abd") {
		t.Fatal("systemScopeKey should differ for different prompts")
	}
	firstContent := contentScopeKey("abc")
	if firstContent != contentScopeKey("abc") {
		t.Fatal("contentScopeKey must be deterministic")
	}

	if !isToolRefusalOutput("I don't have access to local files", nil) {
		t.Fatal("expected tool refusal detection")
	}
	if isToolRefusalOutput("Here is the result", nil) {
		t.Fatal("plain output is not a refusal")
	}
	if !isUnusableModelOutput("", nil) {
		t.Fatal("empty output should be unusable")
	}
	if !isUnusableModelOutput("I seem to be encountering an error", nil) {
		t.Fatal("canned failure should be unusable")
	}
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "read"}}}
	if !isUnusableModelOutput("no access to the filesystem", tools) {
		t.Fatal("tool refusal should be unusable when tools were provided")
	}
	if isUnusableModelOutput("no access to the filesystem", nil) {
		t.Fatal("tool refusal without tools is usable text")
	}
	if isUnusableModelOutput("useful answer", nil) {
		t.Fatal("useful answer should be usable")
	}
}

func TestContentRawString(t *testing.T) {
	raw := contentRawString("hello \"world\"")
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("contentRawString produced invalid JSON: %v", err)
	}
	if decoded != "hello \"world\"" {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	t.Setenv("RATE_LIMIT_MAX", "1")
	t.Setenv("RATE_LIMIT_WINDOW_SECONDS", "60")
	app := fiber.New()
	app.Use(RateLimitMiddleware())
	app.Get("/limited", func(c fiber.Ctx) error { return c.SendString("ok") })

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/limited", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("first request status = %d", resp.StatusCode)
	}
	resp, err = app.Test(httptest.NewRequest(http.MethodGet, "/limited", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", resp.StatusCode)
	}

	// /health is exempt.
	resp, _ = app.Test(httptest.NewRequest(http.MethodGet, "/health", nil))
	if resp.StatusCode != 404 {
		// The route is not registered in this app, but the limiter must not
		// turn it into a 429.
		if resp.StatusCode == fiber.StatusTooManyRequests {
			t.Fatal("/health should bypass the limiter")
		}
	}
}

func TestRegisterRoutesExtraEndpoints(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Setenv("API_TOKEN", "route-token")
	t.Setenv("ADMIN_TOKEN", "route-admin")
	app := fiber.New()
	RegisterRoutes(app)

	paths := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/v1/models", 200},
		{http.MethodGet, "/v1/workers", 200},
		{http.MethodGet, "/metrics", 401},
		{http.MethodGet, "/stats", 200},
		{http.MethodGet, "/history", 200},
		{http.MethodGet, "/media", 200},
		{http.MethodGet, "/status", 200},
		{http.MethodPost, "/reset", 200},
		{http.MethodGet, "/output/missing.png", 404},
		{http.MethodGet, "/output/%2e%2e%2f%2e%2e%2fetc%2fpasswd", 400},
	}
	for _, tc := range paths {
		var body io.Reader
		if tc.method == http.MethodPost {
			body = strings.NewReader(`{}`)
		}
		req := httptest.NewRequest(tc.method, tc.path, body)
		if tc.method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		if resp.StatusCode != tc.status {
			t.Errorf("%s %s status = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.status)
		}
	}
}

func TestInitWorkerPool(t *testing.T) {
	InitWorkerPool()
	if GetWorkerPool() == nil {
		t.Fatal("worker pool should be initialized")
	}
}

func TestExportAtomicAndSchedule(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Chdir(t.TempDir())
	DefaultAnalyticsService.Schedule()
	if err := exportAnalyticsAtomic(); err != nil {
		t.Fatalf("exportAnalyticsAtomic: %v", err)
	}
	if _, err := os.Stat("output/gemini_analytics.json"); err != nil {
		t.Fatalf("analytics JSON missing: %v", err)
	}
	if _, err := os.Stat("output/gemini_analytics.xlsx"); err != nil {
		t.Fatalf("analytics XLSX missing: %v", err)
	}
}

func TestMCPHandleRequest(t *testing.T) {
	cases := []struct {
		name   string
		req    JSONRPCRequest
		expect string
	}{
		{
			name:   "initialize supported",
			req:    JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: json.RawMessage(`{"protocolVersion":"2024-11-05"}`)},
			expect: "2024-11-05",
		},
		{
			name:   "initialize unsupported",
			req:    JSONRPCRequest{JSONRPC: "2.0", ID: 2, Method: "initialize", Params: json.RawMessage(`{"protocolVersion":"1999-01-01"}`)},
			expect: "unsupported protocol version",
		},
		{
			name:   "ping",
			req:    JSONRPCRequest{JSONRPC: "2.0", ID: 3, Method: "ping"},
			expect: `"result"`,
		},
		{
			name:   "tools list",
			req:    JSONRPCRequest{JSONRPC: "2.0", ID: 4, Method: "tools/list"},
			expect: "generate_image",
		},
		{
			name:   "unknown tool",
			req:    JSONRPCRequest{JSONRPC: "2.0", ID: 5, Method: "tools/call", Params: json.RawMessage(`{"name":"nope","arguments":{"prompt":"x"}}`)},
			expect: "isError",
		},
		{
			name:   "unknown method",
			req:    JSONRPCRequest{JSONRPC: "2.0", ID: 6, Method: "does/not/exist"},
			expect: "Method not found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() {
				handleMCPRequest(tc.req, "http://127.0.0.1:1")
			})
			if !strings.Contains(out, tc.expect) {
				t.Fatalf("output %q does not contain %q", out, tc.expect)
			}
		})
	}

	// Notifications with no ID produce no output and must not panic.
	out := captureStdout(t, func() {
		handleMCPRequest(JSONRPCRequest{JSONRPC: "2.0", Method: "notifications/initialized"}, "http://127.0.0.1:1")
		handleMCPRequest(JSONRPCRequest{JSONRPC: "2.0", Method: "unknown/notification"}, "http://127.0.0.1:1")
	})
	if strings.TrimSpace(out) != "" {
		t.Fatalf("notifications should not write to stdout: %q", out)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return string(data)
}

func TestMCPHealthCheckErrorAndRunServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	checkMCPBackendHealth(server.URL)
	checkMCPBackendHealth("http://127.0.0.1:1") // unreachable branch

	t.Setenv("GEMINI_API_URL", server.URL)

	oldIn, oldOut := os.Stdin, os.Stdout
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin, os.Stdout = inR, outW

	go func() {
		_, _ = inW.WriteString(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
		_ = inW.Close()
	}()

	done := make(chan struct{})
	go func() {
		RunMCPServer()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		os.Stdin, os.Stdout = oldIn, oldOut
		t.Fatal("RunMCPServer did not exit on stdin EOF")
	}

	_ = outW.Close()
	data, _ := io.ReadAll(outR)
	os.Stdin, os.Stdout = oldIn, oldOut
	if !strings.Contains(string(data), `"result"`) {
		t.Fatalf("unexpected MCP stdout: %q", string(data))
	}
}

func TestMCPCallToolSwitch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/music":
			_, _ = w.Write([]byte(`{"text":"ok","music":[{"local_path":"/x/a.mp3","title":"T"}]}`))
		default:
			_, _ = w.Write([]byte(`{"text":"ok","images":["http://img/1.png"],"videos":["http://vid/1.mp4"]}`))
		}
	}))
	defer server.Close()

	for _, tool := range []string{"generate_image", "generate_video", "generate_music", "chat"} {
		if _, err := callMCPTool(tool, PromptArg{Prompt: "x"}, server.URL); err != nil {
			t.Errorf("callMCPTool(%s): %v", tool, err)
		}
	}
}

func TestHandleMCPChatWithMedia(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MCP_MEDIA_ROOT", root)
	writeTestPNG(t, root, "chat.png")

	sawMultipart := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			sawMultipart = true
			_ = r.ParseMultipartForm(1 << 20)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"described"}`))
	}))
	defer server.Close()

	res, err := handleMCPChat(PromptArg{Prompt: "describe", RefImagePath: "chat.png"}, server.URL)
	if err != nil {
		t.Fatalf("handleMCPChat: %v", err)
	}
	if !strings.Contains(res.Content[0].Text, "described") {
		t.Fatalf("unexpected response: %+v", res)
	}
	if !sawMultipart {
		t.Fatal("expected a multipart upload")
	}
}

func TestConcurrentExports(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Chdir(t.TempDir())

	app := fiber.New()
	app.Get("/export/excel", HandleExcelExport)
	app.Get("/export/json", HandleJSONExport)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		for _, path := range []string{"/export/excel", "/export/json"} {
			wg.Add(1)
			go func(path string) {
				defer wg.Done()
				resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
				if err != nil {
					t.Errorf("%s: %v", path, err)
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Errorf("%s status = %d, want 200", path, resp.StatusCode)
				}
			}(path)
		}
	}
	wg.Wait()
}
