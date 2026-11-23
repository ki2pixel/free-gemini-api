package gemini

import (
	"strings"
	"testing"
)

func TestModelRegistryResolve(t *testing.T) {
	reg := GetModelRegistry()

	tests := []struct {
		input          string
		expectedID     string
		expectedHex    string
		headerContains string // what to check in BuildHeaderValue (empty = use expectedHex)
	}{
		{"gemini-3.8-flash", "gemini-3.8-flash", "56fdd199312815e2", ""},
		{"flash", "gemini-3.8-flash", "56fdd199312815e2", ""},
		{"gemini-flash", "gemini-3.8-flash", "56fdd199312815e2", ""},
		{"gemini-4-hydrogen", "gemini-4-hydrogen", "56fdd199312815e2", ""},
		{"hydrogen", "gemini-4-hydrogen", "56fdd199312815e2", ""},
		{"hydrogen-task", "gemini-4-hydrogen-task", "56fdd199312815e2", ""},
		{"gemini-4-neon", "gemini-4-neon", "e6fa609c3fa255c0", ""},
		{"neon", "gemini-4-neon", "e6fa609c3fa255c0", ""},
		{"neon-task", "gemini-4-neon-task", "e6fa609c3fa255c0", ""},
		{"neon-high", "gemini-4-neon-thinking-high", "e6fa609c3fa255c0", ",8,2,"},
		{"neon-medium", "gemini-4-neon-thinking-medium", "e6fa609c3fa255c0", ",8,4,"},
		{"neon-low", "gemini-4-neon-thinking-low", "e6fa609c3fa255c0", ",8,1,"},
		{"gemini-auto", "gemini-auto", "8c46e95b1a07cecc", ""},
		{"auto", "gemini-auto", "8c46e95b1a07cecc", ""},
		{"gemini-3.5-flash-lite", "gemini-3.5-flash-lite", "8c46e95b1a07cecc", ""},
		{"flash-lite", "gemini-3.5-flash-lite", "8c46e95b1a07cecc", ""},
		{"lite", "gemini-3.5-flash-lite", "8c46e95b1a07cecc", ""},
		{"gemini-3.1-pro", "gemini-3.1-pro", "e6fa609c3fa255c0", ""},
		{"gemini-pro", "gemini-3.1-pro", "e6fa609c3fa255c0", ""},
		{"pro", "gemini-3.1-pro", "e6fa609c3fa255c0", ""},
		// Argon models: ModeID is NOT embedded in the JSPB header (verified via CDP 2026-10-14).
		// Thinking level sits at index 15: Low=1, Medium=4, High=2
		{"gemini-4-argon", "gemini-4-argon", "95a4c0abcba24364", ",8,1,"},
		{"argon", "gemini-4-argon", "95a4c0abcba24364", ",8,1,"},
		{"argon-high", "gemini-4-argon-thinking-high", "95a4c0abcba24364", ",8,2,"},
		{"argon-medium", "gemini-4-argon-thinking-medium", "95a4c0abcba24364", ",8,4,"},
		{"argon-low", "gemini-4-argon-thinking-low", "95a4c0abcba24364", ",8,1,"},
		{"thinking", "gemini-3.8-thinking", "56fdd199312815e2", ""},
		{"", "gemini-3.8-flash", "56fdd199312815e2", ""},
		{"unknown-model-xyz", "gemini-3.8-flash", "56fdd199312815e2", ""},
	}

	for _, tt := range tests {
		m := reg.Resolve(tt.input)
		if m == nil {
			t.Fatalf("Resolve(%q) returned nil", tt.input)
		}
		if m.ID != tt.expectedID {
			t.Errorf("Resolve(%q) ID = %v, expected %v", tt.input, m.ID, tt.expectedID)
		}
		if m.ModeID != tt.expectedHex {
			t.Errorf("Resolve(%q) ModeID = %v, expected %v", tt.input, m.ModeID, tt.expectedHex)
		}

		headerVal := m.BuildHeaderValue()
		checkStr := tt.headerContains
		if checkStr == "" {
			checkStr = tt.expectedHex
		}
		if !strings.Contains(headerVal, checkStr) {
			t.Errorf("BuildHeaderValue() %q does not contain %q", headerVal, checkStr)
		}
	}
}

func TestModelRegistryCustomRegister(t *testing.T) {
	reg := GetModelRegistry()

	custom := ModelConfig{
		ID:          "gemini-test-custom",
		DisplayName: "Gemini Custom Model",
		ModeID:      "1234567890abcdef",
		Aliases:     []string{"custom-test"},
		Source:      "custom",
	}

	reg.Register(custom)
	defer reg.Unregister("gemini-test-custom")

	resolved := reg.Resolve("custom-test")
	if resolved.ID != "gemini-test-custom" {
		t.Fatalf("Expected resolved custom model ID 'gemini-test-custom', got %s", resolved.ID)
	}
	if resolved.ModeID != "1234567890abcdef" {
		t.Fatalf("Expected ModeID '1234567890abcdef', got %s", resolved.ModeID)
	}
}

// TestArgonThinkingLevels verifies that the JSPB header thinking level values
// match what was observed in live CDP traffic (2026-10-14):
//
//	Low=1, Medium=4, High=2  (at index 15)
func TestArgonThinkingLevels(t *testing.T) {
	reg := GetModelRegistry()

	cases := []struct {
		model     string
		wantLevel string
	}{
		{"argon-low", ",8,1,"},
		{"argon-medium", ",8,4,"},
		{"argon-high", ",8,2,"},
	}

	for _, c := range cases {
		m := reg.Resolve(c.model)
		if m == nil {
			t.Fatalf("Resolve(%q) returned nil", c.model)
		}
		h := m.BuildHeaderValue()
		if !strings.Contains(h, c.wantLevel) {
			t.Errorf("Resolve(%q).BuildHeaderValue() = %q, want to contain %q", c.model, h, c.wantLevel)
		}
		// Argon thinking headers contain ModeID at index 4 and thinking level at index 15
		if !strings.Contains(h, "95a4c0abcba24364") {
			t.Errorf("Resolve(%q).BuildHeaderValue() = %q, should embed ModeID at index 4", c.model, h)
		}
	}
}

// TestHydrogenSingleCanonicalDefinition guards A-16: the built-in catalog must
// contain exactly one definition of gemini-4-hydrogen, and that definition must
// be the canonical thinking-capable one (default thinking level Low=1).
func TestHydrogenSingleCanonicalDefinition(t *testing.T) {
	definitions := 0
	for _, m := range defaultBuiltinModels {
		if strings.EqualFold(strings.TrimSpace(m.ID), "gemini-4-hydrogen") {
			definitions++
		}
	}
	if definitions != 1 {
		t.Fatalf("defaultBuiltinModels contains %d definitions of gemini-4-hydrogen, want exactly 1", definitions)
	}

	m, ok := GetModelRegistry().Get("gemini-4-hydrogen")
	if !ok {
		t.Fatal("Get(gemini-4-hydrogen) failed")
	}

	capabilities := make(map[string]bool, len(m.Capabilities))
	for _, capability := range m.Capabilities {
		capabilities[capability] = true
	}
	for _, want := range []string{"video", "long_context"} {
		if !capabilities[want] {
			t.Errorf("gemini-4-hydrogen capabilities %v missing %q", m.Capabilities, want)
		}
	}

	if header := m.BuildHeaderValue(); !strings.Contains(header, ",8,1,") {
		t.Errorf("gemini-4-hydrogen BuildHeaderValue() = %q, want default thinking Low (contains %q)", header, ",8,1,")
	}
}

// TestUpstreamAntigravityAliases guards A-15: the upstream Antigravity Hub /
// OpenCode identifiers gemini-4-<family>-<tier> must resolve through the alias
// map, including the exact lookups backed by Get and ModelForThinkingTier.
func TestUpstreamAntigravityAliases(t *testing.T) {
	reg := GetModelRegistry()

	cases := []struct {
		upstream      string
		canonicalID   string
		thinkingLevel int
	}{
		{"gemini-4-argon-high", "gemini-4-argon-thinking-high", 2},
		{"gemini-4-argon-medium", "gemini-4-argon-thinking-medium", 4},
		{"gemini-4-argon-low", "gemini-4-argon-thinking-low", 1},
		{"gemini-4-neon-high", "gemini-4-neon-thinking-high", 2},
		{"gemini-4-neon-medium", "gemini-4-neon-thinking-medium", 4},
		{"gemini-4-neon-low", "gemini-4-neon-thinking-low", 1},
		{"gemini-4-hydrogen-high", "gemini-4-hydrogen-thinking-high", 2},
		{"gemini-4-hydrogen-medium", "gemini-4-hydrogen-thinking-medium", 4},
		{"gemini-4-hydrogen-low", "gemini-4-hydrogen-thinking-low", 1},
	}

	for _, tc := range cases {
		t.Run(tc.upstream, func(t *testing.T) {
			got, ok := reg.Get(tc.upstream)
			if !ok {
				t.Fatalf("Get(%q) failed", tc.upstream)
			}
			if got.ID != tc.canonicalID {
				t.Errorf("Get(%q).ID = %q, want %q", tc.upstream, got.ID, tc.canonicalID)
			}
			if got.ThinkingLevel != tc.thinkingLevel {
				t.Errorf("Get(%q).ThinkingLevel = %d, want %d", tc.upstream, got.ThinkingLevel, tc.thinkingLevel)
			}

			resolved := reg.Resolve(tc.upstream)
			if resolved == nil {
				t.Fatalf("Resolve(%q) returned nil", tc.upstream)
			}
			if resolved.ID != tc.canonicalID {
				t.Errorf("Resolve(%q).ID = %q, want %q", tc.upstream, resolved.ID, tc.canonicalID)
			}
			if resolved.ThinkingLevel != tc.thinkingLevel {
				t.Errorf("Resolve(%q).ThinkingLevel = %d, want %d", tc.upstream, resolved.ThinkingLevel, tc.thinkingLevel)
			}
		})
	}

	tier, ok := reg.ModelForThinkingTier("gemini-4-argon-high", "low")
	if !ok {
		t.Fatal(`ModelForThinkingTier("gemini-4-argon-high", "low") failed`)
	}
	if tier != "gemini-4-argon-thinking-low" {
		t.Errorf(`ModelForThinkingTier("gemini-4-argon-high", "low") = %q, want %q`, tier, "gemini-4-argon-thinking-low")
	}
}

func TestNeonThinkingLevelsAndTaskMode(t *testing.T) {
	reg := GetModelRegistry()

	// 1. Verify Neon Thinking Levels match CDP capture (Low=1, Medium=4, High=2)
	cases := []struct {
		model     string
		wantLevel string
	}{
		{"neon-low", ",8,1,"},
		{"neon-medium", ",8,4,"},
		{"neon-high", ",8,2,"},
	}

	for _, c := range cases {
		m := reg.Resolve(c.model)
		if m == nil {
			t.Fatalf("Resolve(%q) returned nil", c.model)
		}
		h := m.BuildHeaderValue()
		if !strings.Contains(h, c.wantLevel) {
			t.Errorf("Resolve(%q).BuildHeaderValue() = %q, want to contain %q", c.model, h, c.wantLevel)
		}
		if !strings.Contains(h, "e6fa609c3fa255c0") {
			t.Errorf("Resolve(%q).BuildHeaderValue() = %q, should embed Neon ModeID e6fa609c3fa255c0", c.model, h)
		}
	}

	// 2. Verify Task Mode header value ([40] for task, [0] for chat)
	taskModel := reg.Resolve("hydrogen-task")
	if taskModel.BuildModeHeaderValue() != "[40]" {
		t.Errorf("hydrogen-task BuildModeHeaderValue() = %q, want [40]", taskModel.BuildModeHeaderValue())
	}
	chatModel := reg.Resolve("hydrogen")
	if chatModel.BuildModeHeaderValue() != "[0]" {
		t.Errorf("hydrogen BuildModeHeaderValue() = %q, want [0]", chatModel.BuildModeHeaderValue())
	}
}
