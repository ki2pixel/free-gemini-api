package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goapi/gemini"

	"github.com/gofiber/fiber/v3"
)

// v3.5.7 Wave P1 regression tests: task-mode routing, per-class metrics and
// the global LRU transcript budget.

func TestTaskModeRouting_ToolBearingRequest(t *testing.T) {
	app := fiber.New()
	app.Post("/resolve", func(c fiber.Ctx) error {
		cfg, model, tier := GetChatService().ResolveModelForRequest(c, c.Query("model"), c.Query("tools") == "true")
		return c.JSON(fiber.Map{
			"id":       cfg.ID,
			"app_mode": cfg.AppMode,
			"model":    model,
			"tier":     tier,
		})
	})

	resolve := func(t *testing.T, model string, tools bool, headers map[string]string) map[string]any {
		t.Helper()
		url := "/resolve?model=" + model
		if tools {
			url += "&tools=true"
		}
		req := httptest.NewRequest(http.MethodPost, url, nil)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("resolve %s: %v", model, err)
		}
		defer resp.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("decode resolve response: %v", err)
		}
		return payload
	}

	t.Run("task alias with tools routes to chat", func(t *testing.T) {
		for requested, wantID := range map[string]string{
			"gemini-4-hydrogen-task": "gemini-4-hydrogen",
			"gemini-4-neon-task":     "gemini-4-neon",
			"hydrogen-task":          "gemini-4-hydrogen",
			"gemini-3.8-flash-task":  "gemini-3.8-flash",
		} {
			payload := resolve(t, requested, true, nil)
			if payload["id"] != wantID {
				t.Errorf("%s: id = %v, want %s", requested, payload["id"], wantID)
			}
			if payload["app_mode"].(float64) != 0 {
				t.Errorf("%s: app_mode = %v, want 0 (Chat mode)", requested, payload["app_mode"])
			}
		}
	})

	t.Run("task alias without tools keeps task mode", func(t *testing.T) {
		payload := resolve(t, "gemini-4-hydrogen-task", false, nil)
		if payload["id"] != "gemini-4-hydrogen-task" {
			t.Fatalf("id = %v, want gemini-4-hydrogen-task", payload["id"])
		}
		if payload["app_mode"].(float64) != 40 {
			t.Fatalf("app_mode = %v, want 40 (Task mode)", payload["app_mode"])
		}
	})

	t.Run("header opt-in keeps task mode with tools", func(t *testing.T) {
		payload := resolve(t, "gemini-4-hydrogen-task", true, map[string]string{
			"X-Allow-Task-Mode-For-Tools": "true",
		})
		if payload["id"] != "gemini-4-hydrogen-task" || payload["app_mode"].(float64) != 40 {
			t.Fatalf("header opt-in was ignored: %v", payload)
		}
	})

	t.Run("environment opt-in keeps task mode with tools", func(t *testing.T) {
		t.Setenv("ALLOW_TASK_MODE_FOR_TOOLS", "true")
		payload := resolve(t, "gemini-4-neon-task", true, nil)
		if payload["id"] != "gemini-4-neon-task" || payload["app_mode"].(float64) != 40 {
			t.Fatalf("environment opt-in was ignored: %v", payload)
		}
	})

	t.Run("X-Gemini-Mode task is also requalified with tools", func(t *testing.T) {
		payload := resolve(t, "gemini-4-hydrogen", true, map[string]string{
			"X-Gemini-Mode": "task",
		})
		if payload["id"] != "gemini-4-hydrogen" || payload["app_mode"].(float64) != 0 {
			t.Fatalf("explicit task mode was not requalified: %v", payload)
		}
	})
}

func TestMetrics_ClassificationAndReplayExposition(t *testing.T) {
	beforeGreeting := counterValue(&turnClassifications, "greeting")
	RecordTurnClassification("greeting")
	RecordTurnClassification("  Greeting  ")
	if got := counterValue(&turnClassifications, "greeting"); got != beforeGreeting+2 {
		t.Fatalf("turn classification counter = %d, want %d", got, beforeGreeting+2)
	}

	beforeReplay := counterValue(&deltaReplays, "conv_mismatch")
	RecordDeltaReplay("conv_mismatch")
	if got := counterValue(&deltaReplays, "conv_mismatch"); got != beforeReplay+1 {
		t.Fatalf("delta replay counter = %d, want %d", got, beforeReplay+1)
	}

	beforeQuarantine := counterValue(&quarantineKinds, "truncated")
	RecordQuarantine("truncated")
	if got := counterValue(&quarantineKinds, "truncated"); got != beforeQuarantine+1 {
		t.Fatalf("quarantine counter = %d, want %d", got, beforeQuarantine+1)
	}

	beforeStops := counterValue(&upstreamSuspectedStops, "gemini-4-neon")
	RecordUpstreamSuspectedStop("gemini-4-neon")
	if got := counterValue(&upstreamSuspectedStops, "gemini-4-neon"); got != beforeStops+1 {
		t.Fatalf("upstream suspected stop counter = %d, want %d", got, beforeStops+1)
	}

	body := string(renderMetricsWithStats(WorkerPoolStats{}))
	for _, want := range []string{
		"# HELP gemini_turn_classification_total",
		"# TYPE gemini_turn_classification_total counter",
		`gemini_turn_classification_total{class="greeting"} `,
		`gemini_turn_classification_total{class="tool_call"} `,
		"# HELP gemini_delta_replay_total",
		`gemini_delta_replay_total{reason="conv_mismatch"} `,
		`gemini_delta_replay_total{reason="forced"} `,
		"# HELP gemini_quarantine_total",
		`gemini_quarantine_total{kind="truncated"} `,
		"# HELP gemini_upstream_suspected_stop_total",
		`gemini_upstream_suspected_stop_total{model="gemini-4-neon"} `,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}

func TestClassifyTurnOutput_Classes(t *testing.T) {
	tools := agenticTestTools()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "tool_call",
			raw:  `<tool_call>{"name":"read","arguments":{"path":"AGENTS.md"}}</tool_call>`,
			want: "tool_call",
		},
		{name: "greeting", raw: "Hello! How can I help you today?", want: "greeting"},
		{name: "incomplete", raw: "Le plan d'action pour", want: "incomplete"},
		{
			name: "refusal",
			raw:  "I must refuse to assist with or generate any content that touches on sexually explicit material, dangerous or illegal activities.",
			want: "refusal",
		},
		{name: "pseudo_report", raw: "Je passe maintenant à la mise à jour de README.md.", want: "pseudo_report"},
		{
			// The Hydrogen final report is also a pseudo-report by lexicon, so
			// the pseudo classifier wins there. This claim uses a completion
			// pair and a cited path that no tool result backs.
			name: "unbacked_completion",
			raw:  "Tous les fichiers `AGENTS.md` ont été validés.",
			want: "unbacked_completion",
		},
		{
			name: "untagged_mutation",
			raw:  readBenchmarkFixture(t, "benchmark_v357_hydrogen_leak.txt"),
			want: "untagged_mutation",
		},
		{
			name: "schema_invalid",
			raw:  `<tool_call>{"name":"read","arguments":{}}</tool_call>`,
			want: "schema_invalid",
		},
		{name: "ok", raw: "Voici la réponse finale.", want: "ok"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arranged := gemini.ArrangeGeminiOutputWithTools(tc.raw, tools)
			if got := classifyTurnOutput(tc.raw, arranged, tools, "classify-"+tc.name); got != tc.want {
				t.Fatalf("classifyTurnOutput(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestPlanSessionTurn_ReplayReasons(t *testing.T) {
	key := "p1-reasons-" + t.Name()
	cleanupSessionTrackerTest(t, key)
	flat := "User: reorganize the documentation\n"

	plan := planSessionTurnWithReason(key, "worker-1", "conv-1", flat)
	if !plan.needReset || plan.reason != "new" {
		t.Fatalf("unknown session reason = %q reset=%v, want new", plan.reason, plan.needReset)
	}

	commitSessionTurn(key, flat, "conv-1", "resp-1", "choice-1", "worker-1")
	if plan = planSessionTurnWithReason(key, "worker-2", "conv-1", flat+"Assistant: x\n"); plan.reason != "worker_mismatch" {
		t.Fatalf("worker mismatch reason = %q, want worker_mismatch", plan.reason)
	}
	if plan = planSessionTurnWithReason(key, "worker-1", "conv-2", flat+"Assistant: x\n"); plan.reason != "conv_mismatch" {
		t.Fatalf("conversation mismatch reason = %q, want conv_mismatch", plan.reason)
	}
	if plan = planSessionTurnWithReason(key, "worker-1", "conv-1", flat); plan.reason != "identical" {
		t.Fatalf("identical transcript reason = %q, want identical", plan.reason)
	}
	if plan = planSessionTurnWithReason(key, "worker-1", "conv-1", "different"); plan.reason != "prefix_mismatch" {
		t.Fatalf("divergent transcript reason = %q, want prefix_mismatch", plan.reason)
	}
	plan = planSessionTurnWithReason(key, "worker-1", "conv-1", flat+"Assistant: x\n")
	if plan.needReset || plan.deltaBytes != len("Assistant: x\n") {
		t.Fatalf("valid delta plan = %+v", plan)
	}
	// A continuation must be distinguishable from a turn that never reached
	// planning, which the summary renders as "none".
	if plan.reason != "delta" {
		t.Fatalf("continuation reason = %q, want delta", plan.reason)
	}
	invalidateSessionTurn(key)
	if plan = planSessionTurnWithReason(key, "worker-1", "conv-1", flat+"Assistant: y\n"); plan.reason != "forced" {
		t.Fatalf("invalidated session reason = %q, want forced", plan.reason)
	}
}

func TestConfigureSessionMemoryBudget(t *testing.T) {
	sessionTurnMu.Lock()
	saved := maxFlatMemorySize
	sessionTurnMu.Unlock()
	t.Cleanup(func() {
		sessionTurnMu.Lock()
		maxFlatMemorySize = saved
		sessionTurnMu.Unlock()
	})

	t.Setenv("SESSION_MEMORY_BUDGET_MB", "7")
	ConfigureSessionMemoryBudget()
	sessionTurnMu.Lock()
	got := maxFlatMemorySize
	sessionTurnMu.Unlock()
	if got != 7*1024*1024 {
		t.Fatalf("configured budget = %d, want 7 MiB", got)
	}

	t.Setenv("SESSION_MEMORY_BUDGET_MB", "not-a-number")
	ConfigureSessionMemoryBudget()
	sessionTurnMu.Lock()
	got = maxFlatMemorySize
	sessionTurnMu.Unlock()
	if got != 7*1024*1024 {
		t.Fatalf("invalid value must keep the previous budget, got %d", got)
	}
}

func TestSessionLRU_GlobalMemoryBudgetEviction(t *testing.T) {
	sessionTurnMu.Lock()
	savedState := sessionTurnState
	savedBytes := totalFlatBytes
	savedBudget := maxFlatMemorySize
	sessionTurnState = map[string]*sessionTurnEntry{}
	totalFlatBytes = 0
	maxFlatMemorySize = 100
	sessionTurnMu.Unlock()
	t.Cleanup(func() {
		sessionTurnMu.Lock()
		sessionTurnState = savedState
		totalFlatBytes = savedBytes
		maxFlatMemorySize = savedBudget
		sessionTurnMu.Unlock()
	})

	flat := func(n int) string { return strings.Repeat("x", n) }
	setAccess := func(key string, when time.Time) {
		t.Helper()
		sessionTurnMu.Lock()
		defer sessionTurnMu.Unlock()
		entry := sessionTurnState[key]
		if entry == nil {
			t.Fatalf("session %q is missing", key)
		}
		entry.lastAccess = when
	}
	has := func(key string) bool {
		t.Helper()
		sessionTurnMu.Lock()
		defer sessionTurnMu.Unlock()
		_, ok := sessionTurnState[key]
		return ok
	}
	total := func() int64 {
		t.Helper()
		sessionTurnMu.Lock()
		defer sessionTurnMu.Unlock()
		return totalFlatBytes
	}

	commitSessionTurn("lru-a", flat(40), "conv-a", "resp-a", "choice-a", "worker-a")
	commitSessionTurn("lru-b", flat(40), "conv-b", "resp-b", "choice-b", "worker-b")
	setAccess("lru-a", time.Now().Add(-2*time.Hour))
	setAccess("lru-b", time.Now().Add(-1*time.Hour))

	// Adding C exceeds the 100-byte budget by 20 bytes: the least recently
	// used session (A) must be evicted first.
	commitSessionTurn("lru-c", flat(40), "conv-c", "resp-c", "choice-c", "worker-c")
	if has("lru-a") {
		t.Fatal("the least recently used session must be evicted first")
	}
	if !has("lru-b") || !has("lru-c") {
		t.Fatal("the newer sessions must survive the first eviction")
	}
	if got := total(); got != 80 {
		t.Fatalf("totalFlatBytes = %d, want 80", got)
	}

	// Refresh B, so C becomes the oldest entry for the next eviction.
	peekSessionTurnConv("lru-b")
	commitSessionTurn("lru-d", flat(40), "conv-d", "resp-d", "choice-d", "worker-d")
	if has("lru-c") {
		t.Fatal("the refreshed session must not be evicted before the older one")
	}
	if !has("lru-b") || !has("lru-d") {
		t.Fatal("the refreshed and newest sessions must survive")
	}

	// Updating an existing session adjusts the byte budget instead of
	// double-counting its old transcript.
	commitSessionTurn("lru-d", flat(70), "conv-d", "resp-d", "choice-d", "worker-d")
	if has("lru-b") {
		t.Fatal("growing D to 70 bytes must evict B to stay under the budget")
	}
	if got := total(); got != 70 {
		t.Fatalf("totalFlatBytes after update = %d, want 70", got)
	}

	// Updating the oldest entry must not let eviction delete that very entry
	// from the map while it is being written back.
	commitSessionTurn("lru-e", flat(40), "conv-e", "resp-e", "choice-e", "worker-e")
	commitSessionTurn("lru-f", flat(40), "conv-f", "resp-f", "choice-f", "worker-f")
	setAccess("lru-e", time.Now().Add(-2*time.Hour))
	setAccess("lru-f", time.Now().Add(-1*time.Hour))
	commitSessionTurn("lru-e", flat(70), "conv-e", "resp-e", "choice-e", "worker-e")
	if !has("lru-e") {
		t.Fatal("the entry being updated must not be evicted")
	}
	if has("lru-f") {
		t.Fatal("the other LRU entry must be evicted to admit the grown transcript")
	}
	sessionTurnMu.Lock()
	grown := len(sessionTurnState["lru-e"].lastFlat)
	sessionTurnMu.Unlock()
	if grown != 70 {
		t.Fatalf("updated transcript length = %d, want 70", grown)
	}

	// A transcript larger than the whole budget cannot be retained. The
	// conversation pointer may stay, but the next turn must replay in full.
	commitSessionTurn("lru-big", flat(120), "conv-big", "resp-big", "choice-big", "worker-big")
	sessionTurnMu.Lock()
	big := sessionTurnState["lru-big"]
	bigLen := 0
	if big != nil {
		bigLen = len(big.lastFlat)
	}
	sessionTurnMu.Unlock()
	if big == nil {
		t.Fatal("the oversized session entry must still exist")
	}
	if bigLen != 0 {
		t.Fatalf("an oversized transcript must not be retained: %d bytes", bigLen)
	}
	plan := planSessionTurnWithReason("lru-big", "worker-big", "conv-big", flat(120))
	if !plan.needReset || plan.reason != "new" {
		t.Fatalf("an oversized transcript must force a full replay: %+v", plan)
	}

	if got := total(); got > maxFlatMemorySize {
		t.Fatalf("global transcript budget exceeded: %d > %d", got, maxFlatMemorySize)
	}
}
