package gemini

import "testing"

func TestResolveReasoningEffort(t *testing.T) {
	cases := []struct {
		input       string
		want        string
		wantDefined bool
	}{
		{"low", "low", true},
		{" minimal ", "low", true},
		{"MEDIUM", "medium", true},
		{"high", "high", true},
		{"none", "none", true},
		{"off", "none", true},
		{"", "", false},
		{"turbo", "", false},
	}
	for _, c := range cases {
		got, defined := ResolveReasoningEffort(c.input)
		if got != c.want || defined != c.wantDefined {
			t.Errorf("ResolveReasoningEffort(%q) = (%q,%v), want (%q,%v)", c.input, got, defined, c.want, c.wantDefined)
		}
	}
}

func TestResolveModel_ReasoningEffort(t *testing.T) {
	cases := []struct{ model, effort, want string }{
		// Thinking families gain the tier variant by name.
		{"gemini-4-neon", "low", "gemini-4-neon-thinking-low"},
		{"gemini-4-neon", "medium", "gemini-4-neon-thinking-medium"},
		{"gemini-4-neon", "high", "gemini-4-neon-thinking-high"},
		{"neon", "high", "gemini-4-neon-thinking-high"},
		{"gemini-4-hydrogen", "high", "gemini-4-hydrogen-thinking-high"},
		{"gemini-4-argon", "medium", "gemini-4-argon-thinking-medium"},
		// "none" drops the explicit tier but keeps the family base model.
		{"gemini-4-neon-thinking-high", "none", "gemini-4-neon"},
		// Models without thinking variants keep the requested model.
		{"gemini-3.5-flash-lite", "high", "gemini-3.5-flash-lite"},
		{"gemini-4-carbon", "high", "gemini-4-carbon"},
		// Absent or unknown effort is ignored.
		{"gemini-4-neon", "", "gemini-4-neon"},
		{"gemini-4-neon", "turbo", "gemini-4-neon"},
	}
	for _, c := range cases {
		if got := ApplyReasoningEffort(c.model, c.effort); got != c.want {
			t.Errorf("ApplyReasoningEffort(%q, %q) = %q, want %q", c.model, c.effort, got, c.want)
		}
	}
}

func TestApplyReasoningEffort_DoesNotMutateRegistry(t *testing.T) {
	registry := GetModelRegistry()
	before := registry.Resolve("gemini-4-neon").ThinkingLevel
	_ = ApplyReasoningEffort("gemini-4-neon", "high")
	_ = ApplyReasoningEffort("gemini-4-neon", "low")
	after := registry.Resolve("gemini-4-neon").ThinkingLevel
	if before != after {
		t.Fatalf("registry entry was mutated: %d -> %d", before, after)
	}
}

func TestEscalateThinkingLevel(t *testing.T) {
	cases := []struct{ model, want string }{
		// Ladder: base -> low -> medium -> high, then a fixed point at high.
		{"gemini-4-neon", "gemini-4-neon-thinking-low"},
		{"gemini-4-neon-thinking-low", "gemini-4-neon-thinking-medium"},
		{"gemini-4-neon-thinking-medium", "gemini-4-neon-thinking-high"},
		{"gemini-4-neon-thinking-high", "gemini-4-neon-thinking-high"},
		// A family without thinking variants is a fixed point.
		{"gemini-4-carbon", "gemini-4-carbon"},
		{"gemini-3.5-flash-lite", "gemini-3.5-flash-lite"},
	}
	for _, c := range cases {
		if got := EscalateThinkingModel(c.model); got != c.want {
			t.Errorf("EscalateThinkingModel(%q) = %q, want %q", c.model, got, c.want)
		}
	}
}

func TestEscalateThinkingLevel_ReachesFixedPoint(t *testing.T) {
	current := "gemini-4-neon"
	seen := []string{current}
	for i := 0; i < 10; i++ {
		next := EscalateThinkingModel(current)
		seen = append(seen, next)
		if next == current {
			if current != "gemini-4-neon-thinking-high" {
				t.Fatalf("escalation stopped at %q, want the top of the ladder", current)
			}
			return
		}
		current = next
	}
	t.Fatalf("escalation did not converge: %v", seen)
}
