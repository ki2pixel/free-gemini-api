package api

import (
	"bytes"
	"encoding/json"
	"goapi/db"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
)

// TestTelemetryPersistence exercises the full asynchronous path: enqueue,
// worker, mapping and SQLite insert. The session key deliberately contains an IP
// address, which must never reach the database in clear.
func TestTelemetryPersistence(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	service := NewHistoryService(16)
	defer service.FlushAndClose()

	const sessionKey = "203.0.113.42|chat:abc123|scopeXYZ"
	const clientSessionID = "203.0.113.42"
	const scopeHash = "scopeXYZ"

	for i := 0; i < 3; i++ {
		base := int64(1000 + i*1000)
		service.LogTelemetry(TurnTelemetry{
			SessionKey:       sessionKey,
			SessionID:        clientSessionID,
			RequestedModel:   "gemini-4-neon",
			EffectiveModel:   "gemini-4-neon",
			SendModel:        "gemini-4-neon",
			SystemScopeHash:  scopeHash,
			UpstreamConvID:   "conv-telemetry-test",
			RequestStartedMs: base,
			CompletedMs:      base + 500,
			ThoughtTokens:    10,
			AnswerTokens:     5,
			TokenEstimator:   "heuristic",
			AttemptCount:     1,
			TurnClass:        "ok",
			FinishReason:     "stop",
		})
	}

	service.FlushAndClose()

	hashed := sessionIDHash(sessionKey)
	if len(hashed) != 64 {
		t.Fatalf("session hash length = %d, want a full 64-character SHA-256 digest", len(hashed))
	}

	var turns, thoughtSum, answerSum int
	if err := db.DB.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(thought_tokens), 0), COALESCE(SUM(answer_tokens), 0)
		 FROM agent_turns WHERE session_id = ?`, hashed,
	).Scan(&turns, &thoughtSum, &answerSum); err != nil {
		t.Fatalf("read persisted turns: %v", err)
	}
	if turns != 3 {
		t.Fatalf("persisted turns = %d, want 3", turns)
	}
	if thoughtSum != 30 || answerSum != 15 {
		t.Fatalf("token sums = (%d, %d), want (30, 15)", thoughtSum, answerSum)
	}

	rows, err := db.DB.Query(`SELECT turn_index FROM agent_turns WHERE session_id = ? ORDER BY turn_index`, hashed)
	if err != nil {
		t.Fatalf("read turn indexes: %v", err)
	}
	defer rows.Close()
	var indexes []int
	for rows.Next() {
		var index int
		if err := rows.Scan(&index); err != nil {
			t.Fatalf("scan turn_index: %v", err)
		}
		indexes = append(indexes, index)
	}
	if len(indexes) != 3 || indexes[0] != 0 || indexes[1] != 1 || indexes[2] != 2 {
		t.Fatalf("turn_index sequence = %v, want [0 1 2]", indexes)
	}

	var scope string
	if err := db.DB.QueryRow(
		`SELECT system_scope_hash FROM agent_sessions WHERE session_id = ?`, hashed,
	).Scan(&scope); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if scope != scopeHash {
		t.Fatalf("system_scope_hash = %q, want %q", scope, scopeHash)
	}

	// Neither the raw IP nor the raw session key may appear anywhere.
	var leaks int
	if err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM agent_sessions
		 WHERE session_id LIKE '%203.0.113.42%' OR user_id LIKE '%203.0.113.42%'`,
	).Scan(&leaks); err != nil {
		t.Fatalf("leak check: %v", err)
	}
	if leaks != 0 {
		t.Fatalf("%d session rows contain the raw IP address", leaks)
	}
	if hashed == clientSessionID {
		t.Fatal("session id was stored in clear instead of hashed")
	}
}

// TestTelemetryDroppedWhenClosed proves the queue is non-blocking and observable
// on the one path that is deterministic: after shutdown.
func TestTelemetryDroppedWhenClosed(t *testing.T) {
	service := NewHistoryService(4)
	service.FlushAndClose()

	before := service.dropped.Load()
	service.LogTelemetry(TurnTelemetry{SessionKey: "closed-session"})
	if got := service.dropped.Load(); got != before+1 {
		t.Fatalf("dropped = %d, want %d: a closed service must count the record instead of blocking", got, before+1)
	}
}

// TestHaltEmitsTurnTelemetry drives the real handler into its 429 circuit
// breaker, which returns before the worker pool. Without this, the most severe
// blocking events would never reach agent_turns.
func TestHaltEmitsTurnTelemetry(t *testing.T) {
	const sessionKey = "telemetry-halt-test"
	cleanupSessionTrackerTest(t, sessionKey)

	tracker := getOrCreateSessionTracker(sessionKey)
	for i := 0; i < doomLoopRepeatHaltThreshold; i++ {
		tracker.RecordToolCall("read", `{"path":"a.go"}`, false)
	}

	var mu sync.Mutex
	var records []TurnTelemetry
	unregister := RegisterTurnTelemetrySink(func(record TurnTelemetry) {
		mu.Lock()
		records = append(records, record)
		mu.Unlock()
	})
	defer unregister()

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
	if resp.StatusCode != fiber.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(records) != 1 {
		t.Fatalf("emitted %d telemetry records for a halt, want exactly 1", len(records))
	}
	record := records[0]
	if record.DoomLoopAction != "halt" {
		t.Fatalf("doom_loop_action = %q, want halt", record.DoomLoopAction)
	}
	if record.TurnClass != "none" || record.FinishReason != "error" {
		t.Fatalf("class/finish = (%q, %q), want (none, error)", record.TurnClass, record.FinishReason)
	}
	if record.AttemptCount != 0 {
		t.Fatalf("attempt_count = %d, want 0 for a request that never reached the pool", record.AttemptCount)
	}
	if record.RequestStartedMs <= 0 || record.CompletedMs < record.RequestStartedMs {
		t.Fatalf("halt timeline = [%d, %d], want a monotone pair", record.RequestStartedMs, record.CompletedMs)
	}
}
