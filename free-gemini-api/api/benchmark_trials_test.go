package api

import (
	"bytes"
	"encoding/json"
	"goapi/db"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

func TestSanitizeBenchmarkTrialID(t *testing.T) {
	valid := []string{
		"legacy-v357:legacy:chat:gemini-4-neon:1",
		"suite.task:S1:task:model-with_dots.and-dashes:12",
	}
	for _, value := range valid {
		if got := sanitizeBenchmarkTrialID(value); got != value {
			t.Errorf("sanitize(%q) = %q, want unchanged", value, got)
		}
	}

	invalid := []string{
		"",
		"   ",
		"has space",
		"inject;rm -rf",
		"line\nbreak",
		"quote\"or'quote",
		"slash/inside",
		"<script>",
		"x:y:z:" + string(bytes.Repeat([]byte("a"), 130)),
	}
	for _, value := range invalid {
		if got := sanitizeBenchmarkTrialID(value); got != "" {
			t.Errorf("sanitize(%q) = %q, want empty", value, got)
		}
	}
}

func TestCORSAllowsBenchmarkTrialHeader(t *testing.T) {
	app := fiber.New()
	app.Use(CORSStrictMiddleware())
	app.Post("/v1/benchmark/trials", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusOK) })

	req := httptest.NewRequest(http.MethodOptions, "/v1/benchmark/trials", nil)
	req.Header.Set("Origin", "http://localhost:8100")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "X-Benchmark-Trial-ID")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	defer resp.Body.Close()

	allowed := resp.Header.Get("Access-Control-Allow-Headers")
	if allowed == "" {
		t.Fatalf("no Access-Control-Allow-Headers in preflight response")
	}
	if !containsHeaderName(allowed, "X-Benchmark-Trial-ID") {
		t.Fatalf("Access-Control-Allow-Headers = %q, missing X-Benchmark-Trial-ID", allowed)
	}
}

func containsHeaderName(list, want string) bool {
	for _, part := range bytes.Split([]byte(list), []byte(",")) {
		if string(bytes.TrimSpace(part)) == want {
			return true
		}
	}
	return false
}

// TestBenchmarkTrialRouteRoundTrip exercises the real asynchronous path: the
// producer enqueues a turn with a trial id, the drain waits for the commit, and
// the verdict endpoint resolves the session from that turn.
func TestBenchmarkTrialRouteRoundTrip(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	const trialID = "route-suite:route-task:chat:gemini-4-neon:1"
	const sessionKey = "route-session-key"
	GetHistoryService().LogTelemetry(TurnTelemetry{
		SessionKey:       sessionKey,
		SessionID:        "route-session-id",
		RequestedModel:   "gemini-4-neon",
		EffectiveModel:   "gemini-4-neon",
		SendModel:        "gemini-4-neon",
		BenchmarkTrialID: trialID,
		ToolsDeclared:    4,
		RequestStartedMs: 1000,
		CompletedMs:      2000,
		InputTokens:      12,
		ThoughtTokens:    3,
		AnswerTokens:     7,
		TokenEstimator:   "heuristic",
		AttemptCount:     1,
		TurnClass:        "ok",
		FinishReason:     "stop",
	})
	GetHistoryService().WaitTelemetryDrain(2 * time.Second)

	app := fiber.New()
	app.Use(AuthMiddleware("secret"))
	app.Post("/v1/benchmark/trials", HandleRecordBenchmarkTrial)
	app.Get("/v1/benchmark/trials", HandleListBenchmarkTrials)

	payload := map[string]any{
		"trial_id":             trialID,
		"suite_id":             "route-suite",
		"task_id":              "route-task",
		"stratum":              "S2",
		"requested_model":      "gemini-4-neon",
		"paradigm":             "chat",
		"trial_index":          1,
		"oracle_passed":        true,
		"oracle_mode":          "hybrid",
		"oracle_exit_code":     0,
		"worktree_diff_files":  1,
		"transcript_clean":     true,
		"wall_time_ms":         1234,
		"primary_failure_code": "none",
	}
	body, _ := json.Marshal(payload)

	post := func(auth string) *http.Response {
		req := httptest.NewRequest(http.MethodPost, "/v1/benchmark/trials", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		return resp
	}

	unauth := post("")
	if unauth.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", unauth.StatusCode)
	}
	unauth.Body.Close()

	invalid := make(map[string]any, len(payload))
	for key, value := range payload {
		invalid[key] = value
	}
	invalid["paradigm"] = "neither"
	invalidBody, _ := json.Marshal(invalid)
	req := httptest.NewRequest(http.MethodPost, "/v1/benchmark/trials", bytes.NewReader(invalidBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("invalid post: %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("invalid payload status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	resp = post("secret")
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("valid post status = %d, want 200", resp.StatusCode)
	}
	var saved db.BenchmarkTrialRecord
	if err := json.NewDecoder(resp.Body).Decode(&saved); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	resp.Body.Close()

	expectedSession := sessionIDHash(sessionKey)
	if saved.SessionID != expectedSession {
		t.Fatalf("correlated session_id = %q, want %q", saved.SessionID, expectedSession)
	}
	if saved.EffectiveModel != "gemini-4-neon" {
		t.Fatalf("effective_model = %q, want gemini-4-neon", saved.EffectiveModel)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/benchmark/trials?suite_id=route-suite&task_id=route-task", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("get status = %d, want 200", resp.StatusCode)
	}
	var listing struct {
		Trials     []db.BenchmarkTrialTelemetry `json:"trials"`
		CostOfPass []db.BenchmarkCostOfPass     `json:"cost_of_pass"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if len(listing.Trials) != 1 {
		t.Fatalf("trials = %d, want 1", len(listing.Trials))
	}
	if listing.Trials[0].TrialID != trialID || listing.Trials[0].InputTokens != 12 {
		t.Fatalf("trial = %+v, want the correlated turn tokens", listing.Trials[0])
	}
	if len(listing.CostOfPass) != 1 || listing.CostOfPass[0].Passes != 1 {
		t.Fatalf("cost_of_pass = %+v, want one passing group", listing.CostOfPass)
	}
}

func TestWaitTelemetryDrainIsBounded(t *testing.T) {
	service := NewHistoryService(4)
	defer service.FlushAndClose()

	start := time.Now()
	service.WaitTelemetryDrain(20 * time.Millisecond)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("empty drain took %v, want an immediate return", elapsed)
	}

	// Simulate a record that never completes: the wait must return at its
	// deadline rather than block the verdict endpoint forever.
	service.telemetryPending.Add(1)
	start = time.Now()
	service.WaitTelemetryDrain(30 * time.Millisecond)
	elapsed := time.Since(start)
	if elapsed < 20*time.Millisecond {
		t.Fatalf("drain returned after %v, before its deadline", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("drain took %v, want a bounded wait", elapsed)
	}
	service.telemetryPending.Add(-1)
}
