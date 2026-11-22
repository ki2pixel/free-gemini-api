package api

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

func TestMetricsPrometheusFormat(t *testing.T) {
	RecordHTTPRequest(299)
	RecordTokenUsage("metrics-format-tier", 42)
	RecordRateLimitDrop()

	stats := WorkerPoolStats{
		Workers: []WorkerStats{
			{
				ID:          "worker-free",
				Tier:        "free",
				Status:      "active",
				InFlight:    2,
				TotalServed: 7,
				TotalErrors: 1,
			},
			{
				ID:                    `worker-"quoted"`,
				Tier:                  "subscription",
				Status:                "cooldown",
				CooldownSecsRemaining: 30,
			},
		},
	}
	body := string(renderMetricsWithStats(stats))

	required := []string{
		"# HELP gemini_http_requests_total",
		"# TYPE gemini_http_requests_total counter",
		`gemini_http_requests_total{status="299"} `,
		"# HELP gemini_tokens_total",
		"# TYPE gemini_tokens_total counter",
		`gemini_tokens_total{tier="metrics-format-tier"} 42`,
		"# HELP gemini_rate_limit_drops_total",
		"# TYPE gemini_rate_limit_drops_total counter",
		"gemini_rate_limit_drops_total ",
		"# HELP gemini_workers_total",
		"# TYPE gemini_workers_total gauge",
		`gemini_workers_total{tier="free"} 1`,
		`gemini_workers_total{tier="subscription"} 1`,
		`gemini_workers_total{tier="ultra"} 0`,
		"# HELP gemini_worker_status",
		"# TYPE gemini_worker_status gauge",
		`gemini_worker_status{id="worker-free",tier="free"} 1`,
		`gemini_worker_status{id="worker-\"quoted\"",tier="subscription"} 0`,
		"# HELP gemini_worker_cooldown_seconds",
		`gemini_worker_cooldown_seconds{id="worker-\"quoted\""} 30`,
		"# HELP gemini_worker_inflight_requests",
		`gemini_worker_inflight_requests{id="worker-free"} 2`,
		"# HELP gemini_worker_requests_total",
		"# TYPE gemini_worker_requests_total counter",
		`gemini_worker_requests_total{id="worker-free"} 7`,
		"# HELP gemini_worker_errors_total",
		"# TYPE gemini_worker_errors_total counter",
		`gemini_worker_errors_total{id="worker-free"} 1`,
		"# HELP gemini_cookie_health_status",
		"# TYPE gemini_cookie_health_status gauge",
		"gemini_cookie_health_status ",
	}
	for _, want := range required {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}

func TestMetricsHandlerAuth(t *testing.T) {
	t.Setenv(PublicMetricsEnv, "false")
	app := fiber.New()
	app.Get("/metrics", MetricsAuthMiddleware("metrics-secret"), HandleMetrics)

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("unauthenticated /metrics = %d, want 401", resp.StatusCode)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer metrics-secret")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("authenticated /metrics = %d, want 200: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain; version=0.0.4") {
		t.Fatalf("Content-Type = %q", got)
	}
	if !strings.Contains(string(body), "gemini_workers_total") {
		t.Fatalf("metrics body missing worker metrics: %s", body)
	}
}

func TestMetricsRouteRegisteredAndAuthorized(t *testing.T) {
	t.Setenv("API_TOKEN", "route-metrics-token")
	t.Setenv("ADMIN_TOKEN", "")
	app := fiber.New()
	RegisterRoutes(app)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer route-metrics-token")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("authorized /metrics = %d, want 200: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "gemini_workers_total") {
		t.Fatalf("metrics body missing worker totals: %s", body)
	}
}

func TestMetricsPublicAccessLoopbackOnly(t *testing.T) {
	t.Setenv(PublicMetricsEnv, "true")

	for _, ip := range []string{"127.0.0.1", "::1", "localhost"} {
		if !publicMetricsAllowedForIP(ip) {
			t.Errorf("loopback IP %q should be allowed", ip)
		}
	}
	for _, ip := range []string{"", "10.0.0.1", "192.168.1.10", "203.0.113.5"} {
		if publicMetricsAllowedForIP(ip) {
			t.Errorf("non-loopback IP %q should be rejected", ip)
		}
	}

	// Fiber's in-memory test transport reports 0.0.0.0, so PUBLIC_METRICS must
	// not bypass authentication there.
	app := fiber.New()
	app.Get("/metrics", MetricsAuthMiddleware("metrics-secret"), HandleMetrics)
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("non-loopback public metrics = %d, want 401", resp.StatusCode)
	}
}

func TestMetricsPublicAccessLoopbackServer(t *testing.T) {
	t.Setenv(PublicMetricsEnv, "true")

	app := fiber.New()
	app.Use(AuthMiddleware(""))
	app.Get("/metrics", MetricsAuthMiddleware(""), HandleMetrics)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		_ = app.Listener(listener)
	}()
	defer func() {
		_ = app.Shutdown()
	}()

	url := "http://" + listener.Addr().String() + "/metrics"
	httpClient := &http.Client{Timeout: 2 * time.Second}
	var resp *http.Response
	for attempt := 0; attempt < 50; attempt++ {
		resp, err = httpClient.Get(url)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /metrics from loopback: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("loopback PUBLIC_METRICS = %d, want 200: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "gemini_cookie_health_status") {
		t.Fatalf("metrics body missing expected series: %s", body)
	}
}

func TestMetricsMiddlewareCountsStatuses(t *testing.T) {
	app := fiber.New()
	app.Use(MetricsMiddleware())
	app.Get("/ok", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusOK) })
	app.Get("/created", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusCreated) })
	app.Get("/teapot", func(c fiber.Ctx) error { return fiber.NewError(fiber.StatusTeapot, "teapot") })

	for _, path := range []string{"/ok", "/created", "/teapot"} {
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	body := string(renderMetricsWithStats(WorkerPoolStats{}))
	for _, want := range []string{
		`gemini_http_requests_total{status="200"} `,
		`gemini_http_requests_total{status="201"} `,
		`gemini_http_requests_total{status="418"} `,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}

func TestMetricsConcurrentRecording(t *testing.T) {
	const workers = 32
	const iterations = 100

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				RecordHTTPRequest(597)
				RecordTokenUsage("metrics-concurrent", 1)
				_ = renderMetricsWithStats(WorkerPoolStats{
					Workers: []WorkerStats{{ID: "w", Tier: "free", Status: "active"}},
				})
			}
		}()
	}
	wg.Wait()

	if got := counterValue(&tokenCounters, "metrics-concurrent"); got != workers*iterations {
		t.Fatalf("token counter = %d, want %d", got, workers*iterations)
	}
	if got := counterValue(&httpRequestCounters, "597"); got < workers*iterations {
		t.Fatalf("http counter = %d, want at least %d", got, workers*iterations)
	}
}
