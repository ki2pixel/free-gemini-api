package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

// TestMaskOldToolObservationsWithStats verifies the byte saving the telemetry
// layer records, and that the masking itself is unchanged.
func TestMaskOldToolObservationsWithStats(t *testing.T) {
	largeBody := strings.Repeat("line of a stale tool result\n", 200)

	messages := []OpenAIChatMessage{{Role: "user", Content: contentRawString("task")}}
	for i := 0; i < 8; i++ {
		messages = append(messages, OpenAIChatMessage{
			Role:       "tool",
			ToolCallID: "call-" + string(rune('a'+i)),
			Content:    contentRawString(largeBody),
		})
	}

	masked, bytesSaved := MaskOldToolObservationsWithStats(messages, defaultObservationMaskKeepRecent, defaultObservationMaskThreshold, 0)
	if bytesSaved <= 0 {
		t.Fatalf("bytesSaved = %d, want > 0 once old observations are compacted", bytesSaved)
	}
	if len(masked) != len(messages) {
		t.Fatalf("masked length = %d, want %d (roles and count preserved)", len(masked), len(messages))
	}

	// The four most recent observations must stay byte-for-byte intact.
	for i := len(messages) - defaultObservationMaskKeepRecent; i < len(messages); i++ {
		if FlattenContent(masked[i].Content) != largeBody {
			t.Fatalf("recent observation %d was modified", i)
		}
	}
	// The input must never be mutated.
	if FlattenContent(messages[1].Content) != largeBody {
		t.Fatal("input slice was mutated")
	}

	// No masking needed: the saving must be zero rather than a stale value.
	small := []OpenAIChatMessage{{Role: "user", Content: contentRawString("hi")}}
	if _, saved := MaskOldToolObservationsWithStats(small, defaultObservationMaskKeepRecent, defaultObservationMaskThreshold, 0); saved != 0 {
		t.Fatalf("bytesSaved = %d for an unmasked transcript, want 0", saved)
	}
}

// TestResolveModelWithRoutingExposesRequalification proves the routing decision
// is observable, so a benchmark arm can be shown to reach the model it names.
func TestResolveModelWithRoutingExposesRequalification(t *testing.T) {
	app := fiber.New()
	app.Post("/resolve", func(c fiber.Ctx) error {
		cfg, model, tier, requestedAppMode, routed := GetChatService().ResolveModelWithRouting(
			c, c.Query("model"), c.Query("tools") == "true")
		return c.JSON(fiber.Map{
			"id":                 cfg.ID,
			"app_mode":           cfg.AppMode,
			"model":              model,
			"tier":               tier,
			"requested_app_mode": requestedAppMode,
			"routed_to_chat":     routed,
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
			t.Fatalf("app.Test: %v", err)
		}
		defer resp.Body.Close()
		var decoded map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return decoded
	}

	// Tool-bearing -task without opt-in: requalified to Chat, and the
	// requalification is now visible to telemetry.
	routed := resolve(t, "gemini-4-hydrogen-task", true, nil)
	if routed["routed_to_chat"] != true {
		t.Fatalf("routed_to_chat = %v, want true", routed["routed_to_chat"])
	}
	if routed["requested_app_mode"] != float64(40) {
		t.Fatalf("requested_app_mode = %v, want 40", routed["requested_app_mode"])
	}
	if routed["app_mode"] != float64(0) {
		t.Fatalf("app_mode = %v, want the Chat mode 0", routed["app_mode"])
	}

	// Explicit opt-in: Task [40] is preserved and nothing was requalified.
	kept := resolve(t, "gemini-4-hydrogen-task", true, map[string]string{"X-Allow-Task-Mode-For-Tools": "true"})
	if kept["routed_to_chat"] != false {
		t.Fatalf("routed_to_chat = %v, want false under opt-in", kept["routed_to_chat"])
	}
	if kept["app_mode"] != float64(40) {
		t.Fatalf("app_mode = %v, want Task mode 40 under opt-in", kept["app_mode"])
	}

	// A Chat request is never requalified.
	chat := resolve(t, "gemini-4-hydrogen", true, nil)
	if chat["routed_to_chat"] != false || chat["requested_app_mode"] != float64(0) {
		t.Fatalf("chat request reported a requalification: %+v", chat)
	}
}
