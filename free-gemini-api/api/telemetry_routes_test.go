package api

import (
	"encoding/json"
	"goapi/db"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

// TestTelemetrySessionsRouteRoundTrip drives the full producer path: a turn with
// a rejected span and a repair is persisted, then read back through the HTTP
// route. The response must carry aggregates, spans and anomalies, and must never
// echo an argument.
func TestTelemetrySessionsRouteRoundTrip(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	const sessionKey = "telemetry-route-session-key"
	service := NewHistoryService(16)
	defer service.FlushAndClose()

	service.LogTelemetry(TurnTelemetry{
		SessionKey:       sessionKey,
		SessionID:        "telemetry-route-client",
		RequestedModel:   "gemini-4-neon",
		EffectiveModel:   "gemini-4-neon",
		SendModel:        "gemini-4-neon",
		RequestStartedMs: 1000,
		CompletedMs:      2000,
		ThoughtTokens:    7,
		AnswerTokens:     5,
		TokenEstimator:   "heuristic",
		AttemptCount:     2,
		TurnClass:        "tool_call",
		FinishReason:     "tool_calls",
		ToolSpans: []db.ToolCallSpanRecord{
			{
				SpanID:            "route-span-ok",
				CallIndex:         0,
				ToolCallID:        "call_ok",
				ToolName:          "read",
				ExtractionChannel: "tagged_xml",
				Disposition:       "emitted",
				ArgsFingerprint:   "fp-ok",
				ArgsBytes:         24,
				TargetHint:        "internal/api/handler.go",
				EmittedAtMs:       1100,
			},
			{
				SpanID:            "route-span-rejected",
				CallIndex:         1,
				ToolCallID:        "call_bad",
				ToolName:          "write",
				IsMutating:        true,
				ExtractionChannel: "tagged_xml",
				Disposition:       "rejected_schema_invalid",
				ArgsFingerprint:   "fp-bad",
				ArgsBytes:         12,
				ValidationError:   "missing required property 'path'",
				EmittedAtMs:       1200,
			},
		},
		Anomalies: []db.TurnAnomalyRecord{
			{AttemptIndex: 1, Category: "arranger_repair", Code: "control_chars", Severity: "info"},
			{AttemptIndex: 2, Category: "arranger_rejection", Code: "schema_invalid", Severity: "warn"},
		},
	})
	service.WaitTelemetryDrain(2 * time.Second)

	sessionID := sessionIDHash(sessionKey)

	app := fiber.New()
	app.Use(AuthMiddleware("secret"))
	app.Get("/v1/telemetry/sessions", HandleTelemetrySessions)

	req := httptest.NewRequest(http.MethodGet, "/v1/telemetry/sessions?session_id="+sessionID, nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var payload struct {
		SessionID string                       `json:"session_id"`
		Sessions  []db.SessionTelemetrySummary `json:"sessions"`
		Spans     []db.ToolCallSpanRecord      `json:"spans"`
		Anomalies []db.TurnAnomalyRecord       `json:"anomalies"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.SessionID != sessionID {
		t.Fatalf("session_id = %q, want %q", payload.SessionID, sessionID)
	}
	if len(payload.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(payload.Sessions))
	}
	summary := payload.Sessions[0]
	if summary.ToolsEmitted != 1 || summary.ToolsBlockedOrThrottled != 1 {
		t.Fatalf("tools emitted/blocked = %d/%d, want 1/1",
			summary.ToolsEmitted, summary.ToolsBlockedOrThrottled)
	}
	if summary.TotalAnomalies != 2 || summary.ArrangerRepairs != 1 {
		t.Fatalf("anomalies/repairs = %d/%d, want 2/1", summary.TotalAnomalies, summary.ArrangerRepairs)
	}
	if len(payload.Spans) != 2 || len(payload.Anomalies) != 2 {
		t.Fatalf("spans/anomalies = %d/%d, want 2/2", len(payload.Spans), len(payload.Anomalies))
	}

	raw, _ := json.Marshal(payload)
	for _, forbidden := range []string{"a.go", "arguments", "secret-payload"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, raw)
		}
	}
}

// TestTelemetrySessionsTrialFilterNeverWildcards proves an explicit trial filter
// cannot silently degrade into the global session list.
func TestTelemetrySessionsTrialFilterNeverWildcards(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	app := fiber.New()
	app.Use(AuthMiddleware("secret"))
	app.Get("/v1/telemetry/sessions", HandleTelemetrySessions)

	for _, trialID := range []string{
		"unknown_trial:task:paradigm:model:1",
		"invalid trial id with spaces",
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/telemetry/sessions?trial_id="+url.QueryEscape(trialID), nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("get %q: %v", trialID, err)
		}
		if resp.StatusCode != fiber.StatusOK {
			resp.Body.Close()
			t.Fatalf("%q: status = %d, want 200", trialID, resp.StatusCode)
		}
		var payload struct {
			SessionID string                       `json:"session_id"`
			Sessions  []db.SessionTelemetrySummary `json:"sessions"`
			Spans     []db.ToolCallSpanRecord      `json:"spans"`
			Anomalies []db.TurnAnomalyRecord       `json:"anomalies"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			t.Fatalf("%q: decode: %v", trialID, err)
		}
		resp.Body.Close()
		if payload.SessionID != "" {
			t.Fatalf("%q: session_id = %q, want empty", trialID, payload.SessionID)
		}
		if len(payload.Sessions) != 0 {
			t.Fatalf("%q: sessions = %d, want an empty list instead of a wildcard",
				trialID, len(payload.Sessions))
		}
		if payload.Spans == nil || payload.Anomalies == nil {
			t.Fatalf("%q: spans/anomalies must be empty arrays, not null", trialID)
		}
	}
}

func TestHelpJSONListsTelemetrySessionsRoute(t *testing.T) {
	app := fiber.New()
	app.Get("/help", HandleHelp)

	req := httptest.NewRequest(http.MethodGet, "/help?format=json", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("get /help: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var doc struct {
		Endpoints []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"endpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]bool{
		"POST /v1/benchmark/trials":  false,
		"GET /v1/benchmark/trials":   false,
		"GET /v1/telemetry/sessions": false,
	}
	for _, endpoint := range doc.Endpoints {
		key := endpoint.Method + " " + endpoint.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Errorf("JSON /help missing endpoint %q", route)
		}
	}
}
