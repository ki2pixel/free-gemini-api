package gemini

import "testing"

func TestThinkingTier(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *ModelConfig
		modelID string
		want    string
	}{
		// A tier-less base of a family has no reasoning budget to report.
		{"base model", &ModelConfig{}, "gemini-4-neon", "none"},
		{"nil config", nil, "gemini-4-neon", "none"},
		{"zero level", &ModelConfig{ThinkingLevel: 0}, "gemini-4-neon", "none"},

		// The configured level mirrors the registry: Low=1, Medium=4, High=2.
		{"level low", &ModelConfig{ThinkingLevel: 1}, "gemini-4-neon", "low"},
		{"level medium", &ModelConfig{ThinkingLevel: 4}, "gemini-4-neon", "medium"},
		{"level high", &ModelConfig{ThinkingLevel: 2}, "gemini-4-neon", "high"},

		// A tier encoded in the model name wins: escalation is expressed as a
		// model name and never mutates the shared registry entry.
		{"suffix low", &ModelConfig{ThinkingLevel: 1}, "gemini-4-neon-thinking-low", "low"},
		{"suffix medium", &ModelConfig{ThinkingLevel: 4}, "gemini-4-neon-thinking-medium", "medium"},
		{"suffix high", &ModelConfig{ThinkingLevel: 2}, "gemini-4-neon-thinking-high", "high"},
		{"suffix wins over level", &ModelConfig{ThinkingLevel: 1}, "gemini-4-neon-thinking-high", "high"},
		{"suffix is case insensitive", &ModelConfig{}, "Gemini-4-Neon-Thinking-Medium", "medium"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ThinkingTier(tc.cfg, tc.modelID); got != tc.want {
				t.Fatalf("ThinkingTier(%q) = %q, want %q", tc.modelID, got, tc.want)
			}
		})
	}
}

// TestThinkingTier_BoundedLabels guarantees the value is always safe as a
// Prometheus label: the metrics layer renders a fixed label set and would show a
// stray value as an extra series.
func TestThinkingTier_BoundedLabels(t *testing.T) {
	allowed := map[string]bool{"none": true, "low": true, "medium": true, "high": true}
	levels := []int{0, 1, 2, 3, 4, 5, -1}
	models := []string{"", "gemini-4-neon", "gemini-4-neon-thinking-low", "custom-model"}
	for _, level := range levels {
		for _, model := range models {
			got := ThinkingTier(&ModelConfig{ThinkingLevel: level}, model)
			if !allowed[got] {
				t.Fatalf("ThinkingTier(level=%d, model=%q) = %q, outside the bounded label set", level, model, got)
			}
		}
	}
}
