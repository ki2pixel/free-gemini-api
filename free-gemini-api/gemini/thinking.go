package gemini

import "strings"

// Thinking tier suffixes understood by the registry. The ladder is ordered by
// actual upstream depth, which is why "medium" precedes "high": Medium=4 is a
// deeper budget than High=2 in the JSPB header.
const (
	thinkingTierNone   = "none"
	thinkingTierLow    = "low"
	thinkingTierMedium = "medium"
	thinkingTierHigh   = "high"
)

// thinkingTierOrder is the escalation ladder used when a technical retry needs
// to raise reasoning capacity: Low -> Medium -> High strictly increases the
// upstream thinking budget, and High is a fixed point.
var thinkingTierOrder = []string{thinkingTierLow, thinkingTierMedium, thinkingTierHigh}

// ResolveReasoningEffort maps an OpenAI `reasoning_effort` value onto a thinking
// tier. The boolean reports whether the value was understood; an unknown or
// empty value must be ignored rather than rejected, because harnesses send this
// field by default and a 400 would break callers that never opted in.
//
// "none" selects the tier-less base model, which is the closest available
// meaning of "no reasoning effort". It is NOT an upstream thinking shutdown:
// BuildHeaderValue floors the level at 1 whenever the header template carries
// %d, so a thinking family still thinks at Low under "none". Only families
// whose template has no %d genuinely stop thinking.
func ResolveReasoningEffort(effort string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low", "minimal":
		return thinkingTierLow, true
	case "medium":
		return thinkingTierMedium, true
	case "high":
		return thinkingTierHigh, true
	case "none", "off", "disabled":
		return thinkingTierNone, true
	default:
		return "", false
	}
}

// ApplyReasoningEffort resolves the model to serve for a request that carries
// `reasoning_effort`. It returns the requested model unchanged when the effort
// is absent, unknown, or unsupported by that model family, so an unsupported
// combination silently keeps the requested model instead of failing the call.
func ApplyReasoningEffort(requested, effort string) string {
	tier, ok := ResolveReasoningEffort(effort)
	if !ok {
		return requested
	}
	resolved, ok := GetModelRegistry().ModelForThinkingTier(requested, tier)
	if !ok {
		return requested
	}
	return resolved
}

// EscalateThinkingModel returns the model one rung up the thinking ladder, used
// when a technical retry must give the model more room to produce an answer.
// It returns the input unchanged when no higher thinking variant exists, which
// makes repeated escalations reach a fixed point at the top of the ladder.
//
// Escalation is expressed as a model name so the shared registry entries are
// never mutated: ModelConfig values are returned by pointer and are read
// concurrently by every in-flight request.
func EscalateThinkingModel(current string) string {
	next := 0
	if tier := thinkingTierOf(strings.ToLower(strings.TrimSpace(current))); tier != "" {
		for index, ladderTier := range thinkingTierOrder {
			if ladderTier == tier {
				next = index + 1
				break
			}
		}
	}
	for ; next < len(thinkingTierOrder); next++ {
		if candidate, ok := GetModelRegistry().ModelForThinkingTier(current, thinkingTierOrder[next]); ok {
			return candidate
		}
	}
	return current
}

// thinkingTierOf reports the tier already encoded in a model name. Non-thinking
// models, including the implicit default of a family, return "".
func thinkingTierOf(model string) string {
	for _, tier := range thinkingTierOrder {
		if strings.HasSuffix(model, "-thinking-"+tier) {
			return tier
		}
	}
	return ""
}

// stripThinkingSuffix removes a trailing "-thinking-<tier>" so the ladder can be
// re-derived from a family base ID.
func stripThinkingSuffix(model string) string {
	for _, tier := range thinkingTierOrder {
		if strings.HasSuffix(model, "-thinking-"+tier) {
			return strings.TrimSuffix(model, "-thinking-"+tier)
		}
	}
	return model
}
