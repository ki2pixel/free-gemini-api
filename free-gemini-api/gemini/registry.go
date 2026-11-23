package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var (
	modelIDRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	modeIDRe     = regexp.MustCompile(`^[0-9a-f]{16}$`)
	headerVerbRe = regexp.MustCompile(`%[^sd%]`)
	validTiers   = map[string]struct{}{"": {}, "free": {}, "subscription": {}, "ultra": {}}
	validSources = map[string]struct{}{"": {}, "builtin": {}, "discovered": {}, "custom": {}}
)

// ValidateModelConfig validates a runtime-supplied model definition before it
// can reach the registry or the on-disk cache.
func ValidateModelConfig(cfg *ModelConfig) error {
	if cfg == nil {
		return errors.New("model config is nil")
	}
	if !modelIDRe.MatchString(cfg.ID) {
		return errors.New("id must match ^[a-z0-9][a-z0-9._-]{0,63}$")
	}
	if cfg.ModeID != "" && !modeIDRe.MatchString(cfg.ModeID) {
		return errors.New("mode_id must be exactly 16 lowercase hexadecimal characters")
	}
	if _, ok := validTiers[cfg.MinTier]; !ok {
		return errors.New("min_tier must be free, subscription, ultra or empty")
	}
	if _, ok := validSources[cfg.Source]; !ok {
		return errors.New("source must be builtin, discovered, custom or empty")
	}
	if len(cfg.DisplayName) > 128 {
		return errors.New("display_name exceeds 128 characters")
	}
	if len(cfg.Description) > 1024 {
		return errors.New("description exceeds 1024 characters")
	}
	if cfg.AppMode != 0 && cfg.AppMode != 40 {
		return errors.New("app_mode must be 0 or 40")
	}
	if cfg.ThinkingLevel < 0 || cfg.ThinkingLevel > 4 {
		return errors.New("thinking_level must be between 0 and 4")
	}
	if len(cfg.HeaderTemplate) > 4096 {
		return errors.New("header_template exceeds 4096 characters")
	}
	if cfg.HeaderTemplate != "" {
		if strings.Count(cfg.HeaderTemplate, "%s") > 1 {
			return errors.New("header_template may contain at most one %s placeholder")
		}
		if strings.Count(cfg.HeaderTemplate, "%d") > 1 {
			return errors.New("header_template may contain at most one %d placeholder")
		}
		if headerVerbRe.MatchString(cfg.HeaderTemplate) {
			return errors.New("header_template contains an unsupported format verb")
		}
		probe := strings.ReplaceAll(cfg.HeaderTemplate, "%s", "56fdd199312815e2")
		probe = strings.ReplaceAll(probe, "%d", "1")
		if !json.Valid([]byte(probe)) {
			return errors.New("header_template must be valid JSON after placeholder substitution")
		}
	}
	if len(cfg.Aliases) > 32 {
		return errors.New("too many aliases (max 32)")
	}
	for _, alias := range cfg.Aliases {
		if !modelIDRe.MatchString(strings.TrimSpace(alias)) {
			return fmt.Errorf("invalid alias %q", alias)
		}
	}
	if len(cfg.Capabilities) > 32 {
		return errors.New("too many capabilities (max 32)")
	}
	for _, capability := range cfg.Capabilities {
		if len(capability) == 0 || len(capability) > 64 {
			return errors.New("capability names must be 1-64 characters")
		}
	}
	return nil
}

// ModelConfig represents a Gemini model configuration
type ModelConfig struct {
	ID             string   `json:"id"`
	DisplayName    string   `json:"display_name"`
	Description    string   `json:"description,omitempty"`
	ModeID         string   `json:"mode_id"`         // Internal hex hash (e.g. 56fdd199312815e2)
	HeaderTemplate string   `json:"header_template"` // JSPB template string
	ThinkingLevel  int      `json:"thinking_level,omitempty"`
	AppMode        int      `json:"app_mode,omitempty"` // 0 = Chat Mode (default), 40 = Task Mode
	MinTier        string   `json:"min_tier"`           // "free" or "subscription"
	Aliases        []string `json:"aliases,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	IsDefault      bool     `json:"is_default"`
	Source         string   `json:"source"` // "builtin", "discovered", "custom"
}

// BuildModeHeaderValue constructs the x-goog-ext-73010989-jspb header value.
// Chat Mode sends [0], Task Mode sends [40] (verified via live CDP).
func (m *ModelConfig) BuildModeHeaderValue() string {
	if m != nil && m.AppMode == 40 {
		return `[40]`
	}
	return `[0]`
}

// BuildHeaderValue constructs the x-goog-ext-525001261-jspb header value.
//
// Format verified via live CDP capture (2026-10-14):
//
//	[1,null,null,null,null,null,null,null,[caps],null,null,null,null,null,8,<thinkingLevel>,null,null,null,null]
//
// Thinking level sits at index 15:
//
//	Low=1, Medium=4, High=2
//
// For non-thinking models the classic short format is used:
//
//	[1,null,null,null,"<modeID>",null,null,0,[caps],null,null,2]
func (m *ModelConfig) BuildHeaderValue() string {
	if m.HeaderTemplate != "" {
		s := m.HeaderTemplate
		if strings.Contains(s, "%s") && strings.Contains(s, "%d") {
			lvl := m.ThinkingLevel
			if lvl <= 0 {
				lvl = 1
			}
			return fmt.Sprintf(s, m.ModeID, lvl)
		}
		if strings.Contains(s, "%d") {
			lvl := m.ThinkingLevel
			if lvl <= 0 {
				lvl = 1
			}
			return fmt.Sprintf(s, lvl)
		}
		if strings.Contains(s, "%s") {
			return fmt.Sprintf(s, m.ModeID)
		}
		return s
	}
	if m.ThinkingLevel > 0 {
		// Argon-style thinking format with ModeID at index 4 and thinking level at index 15
		return fmt.Sprintf(`[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`, m.ModeID, m.ThinkingLevel)
	}
	// Default non-thinking format with modeID
	return fmt.Sprintf(`[1,null,null,null,"%s",null,null,0,[4],null,null,2]`, m.ModeID)
}

// ModelRegistry manages the thread-safe collection of available models
type ModelRegistry struct {
	mu           sync.RWMutex
	models       map[string]*ModelConfig // ID (lowercase) -> ModelConfig
	aliasMap     map[string]string       // Alias (lowercase) -> canonical ID
	defaultModel string
	storagePath  string
}

var (
	globalModelRegistry *ModelRegistry
	registryOnce        sync.Once
)

// Default models catalog
var defaultBuiltinModels = []ModelConfig{
	{
		ID:             "gemini-4-hydrogen-task",
		DisplayName:    "Google Gemini 4 Hydrogen (Task Mode)",
		Description:    "Everyday help with agentic task execution and workflow guidance",
		ModeID:         "56fdd199312815e2",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,0,[4],null,null,2]`,
		AppMode:        40,
		MinTier:        "free",
		Aliases:        []string{"hydrogen-task", "4-hydrogen-task", "everyday-task"},
		Capabilities:   []string{"chat", "stream", "task", "workflow"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-4-neon",
		DisplayName:    "Google Gemini 4 Neon",
		Description:    "Deeper analysis — complex coding, deep logic, and multi-turn problem solving",
		ModeID:         "e6fa609c3fa255c0",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,0,[4],null,null,2]`,
		MinTier:        "subscription",
		Aliases:        []string{"4-neon", "neon", "deeper-analysis", "gemini-neon"},
		Capabilities:   []string{"chat", "stream", "vision", "tools", "complex_reasoning"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-4-neon-task",
		DisplayName:    "Google Gemini 4 Neon (Task Mode)",
		Description:    "Deeper analysis with advanced agentic planning and task execution",
		ModeID:         "e6fa609c3fa255c0",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,0,[4],null,null,2]`,
		AppMode:        40,
		MinTier:        "subscription",
		Aliases:        []string{"neon-task", "4-neon-task"},
		Capabilities:   []string{"chat", "stream", "task", "workflow", "complex_reasoning"},
		IsDefault:      false,
		Source:         "builtin",
	},
	// A-15: upstream Antigravity Hub / OpenCode address thinking models as
	// gemini-4-<family>-<tier>, without the "-thinking-" infix. Each thinking
	// entry below keeps its canonical Go ID and registers the upstream name as an
	// alias, so Get/lookupLocked (used by ModelForThinkingTier) resolve exactly:
	//
	//	gemini-4-argon-high      -> gemini-4-argon-thinking-high      (ThinkingLevel 2)
	//	gemini-4-argon-medium    -> gemini-4-argon-thinking-medium    (ThinkingLevel 4)
	//	gemini-4-argon-low       -> gemini-4-argon-thinking-low       (ThinkingLevel 1)
	//	gemini-4-neon-high       -> gemini-4-neon-thinking-high       (ThinkingLevel 2)
	//	gemini-4-neon-medium     -> gemini-4-neon-thinking-medium     (ThinkingLevel 4)
	//	gemini-4-neon-low        -> gemini-4-neon-thinking-low        (ThinkingLevel 1)
	//	gemini-4-hydrogen-high   -> gemini-4-hydrogen-thinking-high   (ThinkingLevel 2)
	//	gemini-4-hydrogen-medium -> gemini-4-hydrogen-thinking-medium (ThinkingLevel 4)
	//	gemini-4-hydrogen-low    -> gemini-4-hydrogen-thinking-low    (ThinkingLevel 1)
	{
		ID:             "gemini-4-neon-thinking-high",
		DisplayName:    "Google Gemini 4 Neon (Thinking High)",
		Description:    "Deeper analysis with extra thorough thinking depth (High=2)",
		ModeID:         "e6fa609c3fa255c0",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  2,
		MinTier:        "subscription",
		Aliases:        []string{"neon-thinking-high", "neon-high", "gemini-4-neon-high"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code", "complex_tasks"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-4-neon-thinking-medium",
		DisplayName:    "Google Gemini 4 Neon (Thinking Medium)",
		Description:    "Deeper analysis with balanced thinking depth (Medium=4)",
		ModeID:         "e6fa609c3fa255c0",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  4,
		MinTier:        "subscription",
		Aliases:        []string{"neon-thinking-medium", "neon-medium", "gemini-4-neon-medium"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code", "complex_tasks"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-4-neon-thinking-low",
		DisplayName:    "Google Gemini 4 Neon (Thinking Low)",
		Description:    "Deeper analysis with fast and efficient thinking (Low=1)",
		ModeID:         "e6fa609c3fa255c0",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  1,
		MinTier:        "subscription",
		Aliases:        []string{"neon-thinking-low", "neon-low", "gemini-4-neon-low"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code", "complex_tasks"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-auto",
		DisplayName:    "Google Gemini Auto",
		Description:    "Auto — adapts model and reasoning to your needs",
		ModeID:         "8c46e95b1a07cecc",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,0,[4],null,null,2]`,
		MinTier:        "free",
		Aliases:        []string{"auto", "gemini-auto"},
		Capabilities:   []string{"chat", "stream", "vision", "tools", "fastest"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-4-carbon",
		DisplayName:    "Google Gemini 4 Carbon",
		Description:    "Long-horizon deep reasoning and autonomous execution (Ultra Tier)",
		ModeID:         "f44d2958d1f0189a",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,0,[4],null,null,2]`,
		MinTier:        "ultra",
		Aliases:        []string{"4-carbon", "carbon", "gemini-carbon"},
		Capabilities:   []string{"chat", "stream", "deep_reasoning", "tools", "frontier"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-3.5-flash-lite",
		DisplayName:    "Google Gemini 3.5 Flash-Lite",
		Description:    "Fastest answers — ultra low latency quick answers",
		ModeID:         "8c46e95b1a07cecc",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,0,[4],null,null,2]`,
		MinTier:        "free",
		Aliases:        []string{"3.5-flash-lite", "flash-lite", "gemini-flash-lite", "gemini-lite", "lite", "fbb127bbb056c959"},
		Capabilities:   []string{"chat", "stream", "vision", "tools", "fastest"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-3.8-flash",
		DisplayName:    "Google Gemini 3.8 Flash",
		Description:    "All-around help — balanced multi-modal flagship",
		ModeID:         "56fdd199312815e2",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,0,[4],null,null,2]`,
		MinTier:        "free",
		Aliases:        []string{"3.8-flash", "gemini-flash", "flash", "gemini-3.7-flash", "default"},
		Capabilities:   []string{"chat", "stream", "vision", "video", "tools"},
		IsDefault:      true,
		Source:         "builtin",
	},
	{
		// A-16: this is the single canonical definition of gemini-4-hydrogen.
		// It supersedes the older non-thinking duplicate that previously sat at the
		// top of this catalog and was always overwritten by this entry at startup.
		ID:          "gemini-4-hydrogen",
		DisplayName: "Google Gemini 4 Hydrogen",
		Description: "Everyday help — new-generation default model (repoints the 3.8 Flash everyday slot), 1M context",
		// Verified via live capture (2026-11-17): the Gemini web app's "Everyday help 4 Hydrogen"
		// picker entry sends mode hash 56fdd199312815e2 (config key 45676041 = default everyday model).
		// Hydrogen thinking tiers in the UI: Low / Medium / High (no xhigh).
		ModeID:         "56fdd199312815e2",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,1,null,null,null,null]`,
		MinTier:        "free",
		Aliases:        []string{"hydrogen", "4-hydrogen", "gemini-hydrogen", "everyday-help"},
		Capabilities:   []string{"chat", "stream", "vision", "video", "tools", "long_context"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-4-hydrogen-thinking-medium",
		DisplayName:    "Google Gemini 4 Hydrogen (Thinking Medium)",
		Description:    "Everyday help with balanced thinking depth (Medium=4)",
		ModeID:         "56fdd199312815e2",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  4,
		MinTier:        "free",
		Aliases:        []string{"hydrogen-thinking-medium", "hydrogen-medium", "gemini-4-hydrogen-medium"},
		Capabilities:   []string{"chat", "stream", "thinking", "long_context"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-4-hydrogen-thinking-high",
		DisplayName:    "Google Gemini 4 Hydrogen (Thinking High)",
		Description:    "Everyday help with extra thorough thinking depth (High=2)",
		ModeID:         "56fdd199312815e2",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  2,
		MinTier:        "free",
		Aliases:        []string{"hydrogen-thinking-high", "hydrogen-high", "gemini-4-hydrogen-high"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code", "long_context"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-4-hydrogen-thinking-low",
		DisplayName:    "Google Gemini 4 Hydrogen (Thinking Low)",
		Description:    "Everyday help with quick and efficient thinking (Low=1)",
		ModeID:         "56fdd199312815e2",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  1,
		MinTier:        "free",
		Aliases:        []string{"hydrogen-thinking-low", "hydrogen-low", "gemini-4-hydrogen-low"},
		Capabilities:   []string{"chat", "stream", "thinking", "long_context"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-3.1-pro",
		DisplayName:    "Google Gemini 3.1 Pro",
		Description:    "Advanced reasoning — complex coding and multi-turn problem solving",
		ModeID:         "e6fa609c3fa255c0",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,0,[4],null,null,2]`,
		MinTier:        "subscription",
		Aliases:        []string{"3.1-pro", "gemini-pro", "pro", "gemini-3.0-pro", "gemini-2.5-pro", "advanced", "61530e79959ab139"},
		Capabilities:   []string{"chat", "stream", "vision", "tools", "complex_reasoning"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:          "gemini-4-argon",
		DisplayName: "Google Gemini 4 Argon",
		Description: "Complex tasks — next-generation frontier intelligence",
		ModeID:      "95a4c0abcba24364",
		// Default Argon without explicit thinking level (uses Low=1 by default per browser)
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,1,null,null,null,null]`,
		MinTier:        "free",
		Aliases:        []string{"4-argon", "gemini-argon", "argon", "gemini-4", "4", "f44d2958d1f0189a"},
		Capabilities:   []string{"chat", "stream", "vision", "tools", "complex_tasks", "frontier"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:          "gemini-4-argon-thinking-high",
		DisplayName: "Google Gemini 4 Argon (Thinking High)",
		Description: "Complex tasks with extra thorough thinking depth (High=2)",
		ModeID:      "95a4c0abcba24364",
		// Thinking level at index 15: High=2 (verified via CDP)
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  2,
		MinTier:        "free",
		Aliases:        []string{"argon-thinking-high", "argon-high", "thinking-high", "gemini-4-argon-high"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code", "complex_tasks"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:          "gemini-4-argon-thinking-medium",
		DisplayName: "Google Gemini 4 Argon (Thinking Medium)",
		Description: "Complex tasks with balanced thinking depth (Medium=4)",
		ModeID:      "95a4c0abcba24364",
		// Thinking level at index 15: Medium=4 (verified via CDP)
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  4,
		MinTier:        "free",
		Aliases:        []string{"argon-thinking-medium", "argon-medium", "thinking-medium", "gemini-4-argon-medium"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code", "complex_tasks"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:          "gemini-4-argon-thinking-low",
		DisplayName: "Google Gemini 4 Argon (Thinking Low)",
		Description: "Complex tasks with quick and efficient thinking (Low=1)",
		ModeID:      "95a4c0abcba24364",
		// Thinking level at index 15: Low=1 (verified via CDP)
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  1,
		MinTier:        "free",
		Aliases:        []string{"argon-thinking-low", "argon-low", "thinking-low", "gemini-4-argon-low"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code", "complex_tasks"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-3.8-flash-thinking-high",
		DisplayName:    "Google Gemini 3.8 Flash (Thinking High)",
		Description:    "Fast reasoning with extra thorough thinking depth (High=2)",
		ModeID:         "56fdd199312815e2",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  2,
		MinTier:        "free",
		Aliases:        []string{"flash-thinking-high", "flash-high"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-3.8-flash-thinking-medium",
		DisplayName:    "Google Gemini 3.8 Flash (Thinking Medium)",
		Description:    "Fast reasoning with balanced thinking depth (Medium=4)",
		ModeID:         "56fdd199312815e2",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  4,
		MinTier:        "free",
		Aliases:        []string{"flash-thinking-medium", "flash-medium"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-3.8-flash-thinking-low",
		DisplayName:    "Google Gemini 3.8 Flash (Thinking Low)",
		Description:    "Fast reasoning with quick thinking (Low=1)",
		ModeID:         "56fdd199312815e2",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  1,
		MinTier:        "free",
		Aliases:        []string{"flash-thinking-low", "flash-low"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-3.1-pro-thinking-high",
		DisplayName:    "Google Gemini 3.1 Pro (Thinking High)",
		Description:    "Pro reasoning with extra thorough thinking depth (High=2)",
		ModeID:         "e6fa609c3fa255c0",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  2,
		MinTier:        "subscription",
		Aliases:        []string{"pro-thinking-high", "pro-high"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code", "complex_reasoning"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-3.1-pro-thinking-medium",
		DisplayName:    "Google Gemini 3.1 Pro (Thinking Medium)",
		Description:    "Pro reasoning with balanced thinking depth (Medium=4)",
		ModeID:         "e6fa609c3fa255c0",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  4,
		MinTier:        "subscription",
		Aliases:        []string{"pro-thinking-medium", "pro-medium"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code", "complex_reasoning"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-3.1-pro-thinking-low",
		DisplayName:    "Google Gemini 3.1 Pro (Thinking Low)",
		Description:    "Pro reasoning with quick thinking (Low=1)",
		ModeID:         "e6fa609c3fa255c0",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,%d,null,null,null,null]`,
		ThinkingLevel:  1,
		MinTier:        "subscription",
		Aliases:        []string{"pro-thinking-low", "pro-low"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code", "complex_reasoning"},
		IsDefault:      false,
		Source:         "builtin",
	},
	{
		ID:             "gemini-3.8-thinking",
		DisplayName:    "Google Gemini 3.8 Flash Thinking",
		Description:    "Deep reasoning with step-by-step thinking scratchpad trace",
		ModeID:         "56fdd199312815e2",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,0,[4,5,6,8],null,null,1]`,
		ThinkingLevel:  1,
		MinTier:        "subscription",
		Aliases:        []string{"gemini-thinking", "flash-thinking", "thinking"},
		Capabilities:   []string{"chat", "stream", "thinking", "math", "code"},
		IsDefault:      false,
		Source:         "builtin",
	},
}

// GetModelRegistry returns the singleton ModelRegistry
func GetModelRegistry() *ModelRegistry {
	registryOnce.Do(func() {
		storagePath := strings.TrimSpace(os.Getenv("MODELS_REGISTRY_PATH"))
		if storagePath == "" {
			// data/ is a writable volume in the hardened container, while the
			// application root is mounted read-only.
			storagePath = filepath.Join("data", "models.json")
		}
		globalModelRegistry = &ModelRegistry{
			models:       make(map[string]*ModelConfig),
			aliasMap:     make(map[string]string),
			defaultModel: "gemini-3.8-flash",
			storagePath:  storagePath,
		}

		// Register built-in models
		for _, m := range defaultBuiltinModels {
			mCopy := m
			globalModelRegistry.registerInternal(&mCopy)
		}

		// Load custom or discovered models from disk
		globalModelRegistry.LoadFromFile()
	})
	return globalModelRegistry
}

func (r *ModelRegistry) registerInternal(m *ModelConfig) {
	key := strings.ToLower(strings.TrimSpace(m.ID))
	r.models[key] = m

	for _, alias := range m.Aliases {
		aKey := strings.ToLower(strings.TrimSpace(alias))
		if aKey != "" {
			r.aliasMap[aKey] = key
		}
	}

	if m.IsDefault {
		r.defaultModel = key
	}
}

// Register adds or updates a model in the registry and persists to disk.
// Invalid definitions are rejected before they can be served or saved.
func (r *ModelRegistry) Register(m ModelConfig) error {
	if err := ValidateModelConfig(&m); err != nil {
		log.Printf("⚠️ [ModelRegistry] Rejected model %q: %v", m.ID, err)
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.registerInternal(&m)
	if err := r.saveToFileInternal(); err != nil {
		log.Printf("⚠️ [ModelRegistry] Failed to persist model %q: %v", m.ID, err)
	}
	log.Printf("🤖 [ModelRegistry] Registered model: %s (%s, ModeID: %s)", m.ID, m.DisplayName, m.ModeID)
	return nil
}

// Unregister removes a model from the registry and updates disk persistence
func (r *ModelRegistry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := strings.ToLower(strings.TrimSpace(id))
	delete(r.models, key)
	for a, k := range r.aliasMap {
		if k == key {
			delete(r.aliasMap, a)
		}
	}
	_ = r.saveToFileInternal()
}

// ModelForThinkingTier resolves the model of the same family that carries the
// requested thinking tier ("low", "medium", "high"). It reports false when the
// family has no such variant, so callers can leave the request untouched.
//
// The lookup is name-based on purpose: ModelConfig values are shared pointers
// in the registry, and mutating ThinkingLevel on one would race with every
// concurrent request reading it through BuildHeaderValue.
func (r *ModelRegistry) ModelForThinkingTier(baseModel, tier string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	base := strings.ToLower(strings.TrimSpace(baseModel))
	if base == "" {
		return "", false
	}
	baseCfg, ok := r.lookupLocked(base)
	if !ok {
		return "", false
	}
	baseID := stripThinkingSuffix(strings.ToLower(baseCfg.ID))
	if tier == thinkingTierNone {
		return baseID, true
	}
	if tier != thinkingTierLow && tier != thinkingTierMedium && tier != thinkingTierHigh {
		return "", false
	}
	candidate, ok := r.lookupLocked(baseID + "-thinking-" + tier)
	if !ok {
		return "", false
	}
	candidateID := strings.ToLower(candidate.ID)
	// The convenience substring matcher may answer with any member of the
	// family; only accept the entry that really encodes this tier.
	if thinkingTierOf(candidateID) != tier || stripThinkingSuffix(candidateID) != baseID {
		return "", false
	}
	return candidate.ID, true
}

// lookupLocked resolves one name without taking the registry lock. Callers must
// hold it.
func (r *ModelRegistry) lookupLocked(name string) (*ModelConfig, bool) {
	clean := strings.ToLower(strings.TrimSpace(name))
	if clean == "" {
		return nil, false
	}
	if model, ok := r.models[clean]; ok {
		return model, true
	}
	if canonicalID, ok := r.aliasMap[clean]; ok {
		if model, ok := r.models[canonicalID]; ok {
			return model, true
		}
	}
	return nil, false
}

// Resolve resolves a model name or alias against the registry, with a small
// set of family convenience matches (see the body for the heuristics).
func (r *ModelRegistry) Resolve(name string) *ModelConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	clean := strings.ToLower(strings.TrimSpace(name))
	if clean == "" {
		if def, ok := r.models[r.defaultModel]; ok {
			return def
		}
		// Fallback to first available
		for _, m := range r.models {
			return m
		}
	}

	// 1. Direct ID match
	if m, ok := r.models[clean]; ok {
		return m
	}

	// 2. Direct Alias match
	if canonicalID, ok := r.aliasMap[clean]; ok {
		if m, ok := r.models[canonicalID]; ok {
			return m
		}
	}

	// 3. Substring matching for convenience
	if strings.Contains(clean, "neon") {
		if strings.Contains(clean, "high") || strings.Contains(clean, "3") {
			if m, ok := r.models["gemini-4-neon-thinking-high"]; ok {
				return m
			}
		}
		if strings.Contains(clean, "med") || strings.Contains(clean, "4") {
			if m, ok := r.models["gemini-4-neon-thinking-medium"]; ok {
				return m
			}
		}
		if strings.Contains(clean, "low") || strings.Contains(clean, "1") {
			if m, ok := r.models["gemini-4-neon-thinking-low"]; ok {
				return m
			}
		}
		if strings.Contains(clean, "task") {
			if m, ok := r.models["gemini-4-neon-task"]; ok {
				return m
			}
		}
		if m, ok := r.models["gemini-4-neon"]; ok {
			return m
		}
	}
	if strings.Contains(clean, "hydrogen") {
		if strings.Contains(clean, "task") {
			if m, ok := r.models["gemini-4-hydrogen-task"]; ok {
				return m
			}
		}
		if m, ok := r.models["gemini-4-hydrogen"]; ok {
			return m
		}
	}
	if strings.Contains(clean, "auto") {
		if m, ok := r.models["gemini-auto"]; ok {
			return m
		}
	}
	if strings.Contains(clean, "argon") {
		if strings.Contains(clean, "high") || strings.Contains(clean, "3") {
			if m, ok := r.models["gemini-4-argon-thinking-high"]; ok {
				return m
			}
		}
		if strings.Contains(clean, "med") || strings.Contains(clean, "2") {
			if m, ok := r.models["gemini-4-argon-thinking-medium"]; ok {
				return m
			}
		}
		if strings.Contains(clean, "low") || strings.Contains(clean, "1") {
			if m, ok := r.models["gemini-4-argon-thinking-low"]; ok {
				return m
			}
		}
		if m, ok := r.models["gemini-4-argon"]; ok {
			return m
		}
	}
	if strings.Contains(clean, "carbon") {
		if m, ok := r.models["gemini-4-carbon"]; ok {
			return m
		}
	}
	if strings.Contains(clean, "lite") {
		if m, ok := r.models["gemini-3.5-flash-lite"]; ok {
			return m
		}
	}
	if strings.Contains(clean, "hydrogen") {
		if strings.Contains(clean, "high") {
			if m, ok := r.models["gemini-4-hydrogen-thinking-high"]; ok {
				return m
			}
		}
		if strings.Contains(clean, "med") {
			if m, ok := r.models["gemini-4-hydrogen-thinking-medium"]; ok {
				return m
			}
		}
		if strings.Contains(clean, "low") {
			if m, ok := r.models["gemini-4-hydrogen-thinking-low"]; ok {
				return m
			}
		}
		if m, ok := r.models["gemini-4-hydrogen"]; ok {
			return m
		}
	}
	if strings.Contains(clean, "pro") {
		if canonicalID, ok := r.aliasMap["pro"]; ok {
			if m, ok := r.models[canonicalID]; ok {
				return m
			}
		}
	}
	if strings.Contains(clean, "thinking") {
		if canonicalID, ok := r.aliasMap["thinking"]; ok {
			if m, ok := r.models[canonicalID]; ok {
				return m
			}
		}
	}
	if strings.Contains(clean, "flash") {
		if m, ok := r.models["gemini-3.8-flash"]; ok {
			return m
		}
	}

	// Fallback to default
	if def, ok := r.models[r.defaultModel]; ok {
		return def
	}
	return &defaultBuiltinModels[0]
}

// Get looks up a model by ID or alias without fallback
func (r *ModelRegistry) Get(name string) (*ModelConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	clean := strings.ToLower(strings.TrimSpace(name))
	if m, ok := r.models[clean]; ok {
		return m, true
	}
	if canonicalID, ok := r.aliasMap[clean]; ok {
		if m, ok := r.models[canonicalID]; ok {
			return m, true
		}
	}
	return nil, false
}

// GetAll returns a list of all registered models
func (r *ModelRegistry) GetAll() []ModelConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]ModelConfig, 0, len(r.models))
	for _, m := range r.models {
		list = append(list, *m)
	}
	return list
}

// LoadFromFile loads custom and discovered models from models.json
func (r *ModelRegistry) LoadFromFile() {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(r.storagePath)
	if err != nil {
		// Backwards compatibility: older installations stored the registry at
		// ./models.json or ./gemini/models.json.
		candidates := []string{"models.json", filepath.Join("gemini", "models.json")}
		for _, altPath := range candidates {
			if altPath == r.storagePath {
				continue
			}
			if altData, altErr := os.ReadFile(altPath); altErr == nil {
				data = altData
				err = nil
				break
			}
		}
		if err != nil {
			return
		}
	}

	var customModels []ModelConfig
	if err := json.Unmarshal(data, &customModels); err != nil {
		log.Printf("⚠️ [ModelRegistry] Failed to parse %s: %v", r.storagePath, err)
		return
	}

	count := 0
	for _, m := range customModels {
		mCopy := m
		r.registerInternal(&mCopy)
		count++
	}
	if count > 0 {
		log.Printf("🤖 [ModelRegistry] Loaded %d custom/discovered models from %s", count, r.storagePath)
	}
}

func (r *ModelRegistry) saveToFileInternal() error {
	var toSave []ModelConfig
	for _, m := range r.models {
		// Save non-builtin or customized models
		if m.Source != "builtin" {
			toSave = append(toSave, *m)
		}
	}

	if len(toSave) == 0 {
		return nil
	}

	data, err := json.MarshalIndent(toSave, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(r.storagePath, data, 0600)
}

// SaveToFile exports the current registry to a specific file path
func (r *ModelRegistry) SaveToFile(filePath string) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var all []ModelConfig
	for _, m := range r.models {
		all = append(all, *m)
	}

	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filePath, data, 0600)
}
