package gemini

import (
	"strings"
	"testing"
)

func TestValidateModelConfigAcceptsValidDefinition(t *testing.T) {
	cfg := &ModelConfig{
		ID:             "gemini-custom-model",
		DisplayName:    "Gemini Custom Model",
		ModeID:         "1234567890abcdef",
		HeaderTemplate: `[1,null,null,null,"%s",null,null,0,[4],null,null,2]`,
		MinTier:        "free",
		Source:         "custom",
		Aliases:        []string{"custom-alias"},
		Capabilities:   []string{"chat", "stream"},
	}
	if err := ValidateModelConfig(cfg); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestValidateModelConfigRejectsUnsafeDefinitions(t *testing.T) {
	base := func() *ModelConfig {
		return &ModelConfig{
			ID:          "gemini-custom-model",
			DisplayName: "Gemini Custom Model",
			ModeID:      "1234567890abcdef",
			MinTier:     "free",
			Source:      "custom",
		}
	}

	cases := []struct {
		name   string
		mutate func(*ModelConfig)
	}{
		{"uppercase id", func(c *ModelConfig) { c.ID = "Gemini-Custom" }},
		{"space in id", func(c *ModelConfig) { c.ID = "gemini custom" }},
		{"path in id", func(c *ModelConfig) { c.ID = "../gemini" }},
		{"short mode id", func(c *ModelConfig) { c.ModeID = "1234" }},
		{"non-hex mode id", func(c *ModelConfig) { c.ModeID = "zzzzzzzzzzzzzzzz" }},
		{"invalid tier", func(c *ModelConfig) { c.MinTier = "enterprise" }},
		{"invalid source", func(c *ModelConfig) { c.Source = "remote" }},
		{"invalid app mode", func(c *ModelConfig) { c.AppMode = 7 }},
		{"invalid thinking level", func(c *ModelConfig) { c.ThinkingLevel = 9 }},
		{"invalid header json", func(c *ModelConfig) { c.HeaderTemplate = `[1,"%s"` }},
		{"too many placeholders", func(c *ModelConfig) { c.HeaderTemplate = `["%s","%s"]` }},
		{"unsupported verb", func(c *ModelConfig) { c.HeaderTemplate = `["%v"]` }},
		{"too many aliases", func(c *ModelConfig) {
			c.Aliases = make([]string, 33)
			for i := range c.Aliases {
				c.Aliases[i] = "alias"
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(cfg)
			if err := ValidateModelConfig(cfg); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestRegistryRejectsInvalidModel(t *testing.T) {
	reg := GetModelRegistry()
	cfg := ModelConfig{
		ID:          "invalid model id",
		DisplayName: "Invalid",
		ModeID:      "1234567890abcdef",
		MinTier:     "free",
		Source:      "custom",
	}
	if err := reg.Register(cfg); err == nil {
		t.Fatal("Register accepted an invalid model id")
	}
	if _, ok := reg.Get(cfg.ID); ok {
		t.Fatal("invalid model was added to the registry")
	}
}

func TestValidateModelConfigRejectsOversizedTemplate(t *testing.T) {
	cfg := &ModelConfig{
		ID:             "gemini-custom-model",
		DisplayName:    "Gemini Custom Model",
		HeaderTemplate: `["` + strings.Repeat("a", 5000) + `"]`,
		MinTier:        "free",
		Source:         "custom",
	}
	if err := ValidateModelConfig(cfg); err == nil {
		t.Fatal("expected oversized template to be rejected")
	}
}
