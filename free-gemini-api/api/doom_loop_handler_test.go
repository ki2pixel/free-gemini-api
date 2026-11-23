package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestHandleOpenAIChatCompletions_DoomLoopCircuitBreaker(t *testing.T) {
	const sessionKey = "handler-doom-loop-test"
	cleanupSessionTrackerTest(t, sessionKey)

	tracker := getOrCreateSessionTracker(sessionKey)
	for i := 0; i < doomLoopRepeatHaltThreshold; i++ {
		tracker.RecordToolCall("read", `{"path":"a.go"}`, false)
	}

	payload := map[string]any{
		"model": "gemini-4-hydrogen",
		"messages": []map[string]any{
			{"role": "user", "content": "inspect the repository"},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"id":   "call_1",
					"type": "function",
					"function": map[string]any{
						"name":      "read",
						"arguments": `{"path":"a.go"}`,
					},
				}},
			},
			{"role": "tool", "tool_call_id": "call_1", "name": "read", "content": "file contents"},
		},
		"stream": false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	app.Post("/v1/chat/completions", HandleOpenAIChatCompletions)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-ID", sessionKey)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != fiber.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d: %s", resp.StatusCode, fiber.StatusTooManyRequests, respBody)
	}
	for _, want := range []string{"doom-loop detected", "repeated 5 times"} {
		if !strings.Contains(string(respBody), want) {
			t.Errorf("circuit-breaker response %q does not contain %q", respBody, want)
		}
	}
}

func TestHandleOpenAIChatCompletions_NewUserTurnResetsBreaker(t *testing.T) {
	const sessionKey = "handler-doom-loop-reset"
	cleanupSessionTrackerTest(t, sessionKey)

	tracker := getOrCreateSessionTracker(sessionKey)
	for i := 0; i < doomLoopRepeatHaltThreshold; i++ {
		tracker.RecordToolCall("read", `{"path":"a.go"}`, false)
	}

	// A new user message is an explicit task boundary: the handler must reset
	// the halted tracker instead of returning 429. The oversized prompt then
	// fails the 1 MiB guard (413) before any worker is needed, proving the
	// request passed the circuit-breaker check.
	payload := map[string]any{
		"model": "gemini-4-hydrogen",
		"messages": []map[string]any{
			{"role": "user", "content": strings.Repeat("x", maxPromptBytes+1)},
		},
		"stream": false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	app.Post("/v1/chat/completions", HandleOpenAIChatCompletions)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-ID", sessionKey)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != fiber.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (circuit breaker was not reset): %s", resp.StatusCode, fiber.StatusRequestEntityTooLarge, respBody)
	}
	if action, _ := tracker.CheckIntervention(); action != ActionNone {
		t.Fatalf("tracker after new user turn = %q, want %q", action, ActionNone)
	}
}
