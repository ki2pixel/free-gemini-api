package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"goapi/gemini"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

const maxPromptBytes = 1 << 20 // 1 MiB

// EstimateTokens calculates an accurate token count using word and character heuristics
func EstimateTokens(text string) int {
	clean := strings.TrimSpace(text)
	if clean == "" {
		return 0
	}
	words := len(strings.Fields(clean))
	runes := len([]rune(clean))

	// Word-based heuristic: ~1.3 tokens per word (standard across English & Code)
	// Character-based fallback: max(1, (runes + 3) / 4)
	wTokens := int(float64(words) * 1.3)
	cTokens := (runes + 3) / 4

	tokens := wTokens
	if cTokens > tokens {
		tokens = cTokens
	}
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// formatSchemaRetryDirective builds the abstract-error retry prompt used when
// the upstream model emitted a tool call that violates its declared schema.
// The broken raw tokens are never replayed; only the actionable violation is
// sent back, together with the metadata-first ordering reminder.
func formatSchemaRetryDirective(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "invalid tool arguments"
	}
	return fmt.Sprintf("[SYSTEM DIRECTIVE: Previous tool call failed validation: %s. Re-issue the tool call correcting this parameter, place 'path' first in the arguments object, and include every required property.]\n\n", detail)
}

// formatTruncationRetryDirective builds the abstract retry prompt used when
// the upstream model emitted a file mutation cut off mid-value. The partial
// payload is never repaired or executed; the model must re-issue a complete
// call with every required argument.
func formatTruncationRetryDirective() string {
	return "[SYSTEM DIRECTIVE: The previous tool call was truncated mid-value and was not executed. Re-issue the complete tool call with all required arguments, including the full path, oldString/newString or content. Do not emit a partial file mutation.]\n\n"
}

// formatPseudoReportRetryDirective builds the corrective prompt used when the
// model answers a tool-bearing request with a conversational progress update or
// completion summary instead of calling the next required tool. The summary is
// never committed; the retry forces the model back into the tool loop with a
// positive availability contract, never by naming a hallucinated quota.
func formatPseudoReportRetryDirective() string {
	return "[SYSTEM DIRECTIVE: Every tool in the catalog is active turn after turn. Do not emit conversational progress updates, do not claim the plan is formalised or applied, and do not synthesize summaries while planned modifications remain pending. Execute the next step immediately by emitting a <tool_call> XML block.]\n\n"
}

// formatUnbackedCompletionRetryDirective builds the corrective prompt used when
// a completion claim cites a path that no successful tool result confirmed, or
// claims that tests/builds passed without a confirmed validation command.
func formatUnbackedCompletionRetryDirective() string {
	return "[SYSTEM DIRECTIVE: The previous message claimed completion without confirmed evidence in this session. Do not claim that a file was updated unless its tool result confirms the write succeeded, and do not claim that tests, builds or audits passed unless a shell result confirms the command actually ran successfully. Continue the plan with the next required <tool_call>; if a planned file or verification is still pending, execute it now.]\n\n"
}

// unlimitedToolExecutionDirective is prepended to every full replay after an
// unusable tool-bearing turn. It restates the execution contract positively:
// no forbidden quota literal is ever sent to the model.
const unlimitedToolExecutionDirective = "[SYSTEM DIRECTIVE: Every tool in the catalog is active turn after turn. Execute the next step immediately by emitting a <tool_call> XML block. Complete the current task directly with tool calls. Do not stop or synthesize until modifications and verifications are complete.]\n\n"

// unusableActionErrorMessage is the single end-of-retries message shared by
// the streaming error chunk and the non-streaming 502 response.
const unusableActionErrorMessage = "upstream model output was unusable with tools (greeting, pseudo-report, refusal, unbacked completion, or incomplete action without tool calls after retries)"

// newStreamErrorChunk builds the terminal SSE error chunk. Its finish_reason
// is always "error": OpenAI-compatible hosts must treat the turn as a failed
// generation, not as a successful model stop. OpenCode 2.4.0 maps
// finish_reason "error" to an AIError (raising its error handling and retries)
// while "stop" is accepted as a completed response, which silently ends the
// task on what is actually a proxy-side failure.
func newStreamErrorChunk(id, model string, created int64, content string) OpenAIChatCompletionChunk {
	return OpenAIChatCompletionChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []OpenAIStreamChoice{
			{
				Index: 0,
				Delta: OpenAIDelta{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "error",
			},
		},
	}
}

const (
	maxToolChatAttempts    = 4
	maxPlainStreamAttempts = 1
	maxPlainChatAttempts   = 2
	// maxTransportRetries is the dedicated budget for upstream transport and
	// session incidents. It is independent from the model generation budget:
	// a 1097 expiry or a 45s silent stall must never consume an attempt meant
	// to recover from a refusal, pseudo-report, schema error, or truncation.
	maxTransportRetries = 2
	// maxEmptyGenerationRetries is the dedicated budget for purely technical
	// aborted generations: empty output or a thought-only turn with no visible
	// content and no tool call. Like transport incidents, these retries reset
	// the upstream conversation without consuming a functional attempt.
	maxEmptyGenerationRetries = 2
)

// maxChatAttempts returns the number of functional upstream generations
// allowed for one request. Transport incidents are retried from a separate
// budget (see shouldRetryTransport) and never reduce this count.
func maxChatAttempts(streaming, hasTools bool) int {
	if hasTools {
		return maxToolChatAttempts
	}
	if streaming {
		return maxPlainStreamAttempts
	}
	return maxPlainChatAttempts
}

// maxChatLoopIterations is the monotonic upper bound of the generation loop:
// functional attempts plus the dedicated transport and empty-generation
// retries. For a tool-bearing request it is guaranteed to stay at or below 8,
// so a pathological upstream can never keep the handler spinning.
func maxChatLoopIterations(streaming, hasTools bool) int {
	return maxChatAttempts(streaming, hasTools) + maxTransportRetries + maxEmptyGenerationRetries
}

// isTransportIncident reports whether an upstream failure is a transport or
// session incident rather than a model generation failure: Google's 1097
// conversation expiry or a stream that went silent past the stall timeout.
func isTransportIncident(err error) bool {
	return errors.Is(err, gemini.ErrConversationExpired) || errors.Is(err, gemini.ErrUpstreamStreamStalled)
}

// shouldRetryTransport reports whether err must be charged to the dedicated
// transport budget. When true, the caller resets the upstream conversation and
// retries without consuming the functional generation attempt.
func shouldRetryTransport(err error, transportRetries int) bool {
	return err != nil && isTransportIncident(err) && transportRetries < maxTransportRetries
}

// recordArrangerRepairs exposes the repair kinds reported by the arranger as
// Prometheus counters.
func recordArrangerRepairs(arranged *gemini.ArrangedOutput) {
	if arranged == nil {
		return
	}
	for _, kind := range arranged.Repairs {
		RecordArrangerRepair(kind)
	}
}

// logFingerprint returns a short, stable, non-reversible identifier for logs.
// Session keys and upstream conversation ids may carry client identifiers, so
// the structured turn summary never prints them verbatim.
func logFingerprint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "none"
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:6])
}

// classifyTurnOutput maps an upstream generation to the bounded class set
// exposed on /metrics. The order mirrors the handler's interception order so
// the metric explains why a turn was retried or blocked.
func classifyTurnOutput(raw string, arranged *gemini.ArrangedOutput, tools []map[string]any, sessionKey string) string {
	lowerRaw := strings.ToLower(raw)
	for _, phrase := range []string{
		"hard time fulfilling your request",
		"i seem to be encountering an error",
	} {
		if strings.Contains(lowerRaw, phrase) {
			return "refusal"
		}
	}
	if gemini.HasTruncatedMutationToolCall(raw) {
		return "untagged_mutation"
	}
	if arranged != nil && arranged.UntaggedMutation {
		return "untagged_mutation"
	}
	if len(tools) > 0 {
		if invalid, _ := gemini.HasInvalidSchemaToolCall(raw, tools); invalid {
			return "schema_invalid"
		}
	}
	if arranged != nil && (arranged.IsToolCall || len(arranged.ToolCalls) > 0) {
		return "tool_call"
	}
	clean := raw
	if arranged != nil {
		clean = arranged.CleanText
	}
	if strings.TrimSpace(clean) == "" {
		return "incomplete"
	}
	if isConversationalReset(clean, tools) {
		return "greeting"
	}
	if isIncompleteFinalProse(clean, tools) {
		return "incomplete"
	}
	if len(tools) > 0 && isToolRefusalOutput(raw, tools) {
		return "refusal"
	}
	if len(tools) > 0 && isPseudoProgressReport(raw, tools) {
		return "pseudo_report"
	}
	if len(tools) > 0 && isUnbackedCompletion(raw, tools, sessionKey) {
		return "unbacked_completion"
	}
	return "ok"
}

// recordTurnClass records the classification and, for shapes that indicate an
// interrupted upstream generation, the suspected-stop counter.
func recordTurnClass(class, model string) {
	RecordTurnClassification(class)
	if class == "greeting" || class == "incomplete" {
		RecordUpstreamSuspectedStop(model)
	}
}

// emitTurnSummary writes the structured, content-free turn line. Only lengths,
// hashes and normalized labels are emitted: never the prompt body, tool
// arguments, or raw conversation ids.
func emitTurnSummary(sessionKey, model string, promptLen, deltaLen int, replayReason, convID string, visibleLen, toolCount int, class, finish string) {
	if strings.TrimSpace(replayReason) == "" {
		replayReason = "none"
	}
	log.Printf("📊 [TURN SUMMARY] session=%s model=%s prompt_len=%d delta_len=%d replay_reason=%s conv_id=%s visible_len=%d tools=%d class=%s finish=%s",
		logFingerprint(sessionKey), model, promptLen, deltaLen, replayReason,
		logFingerprint(convID), visibleLen, toolCount, class, finish)
}

// arrangerRejectionReason classifies a schema-validation detail into the
// bounded Prometheus label set.
func arrangerRejectionReason(detail string) string {
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "not declared"),
		strings.Contains(lower, "undeclared"),
		strings.Contains(lower, "unknown tool"):
		return "undeclared_tool"
	default:
		return "schema_invalid"
	}
}

// firstUserMessageContent returns the flattened text of the conversation's
// first user message. Clients that send no explicit affinity identifier are
// scoped per chat by this value, so separate chats get separate upstream
// conversations while the turns of one chat stay together.
func firstUserMessageContent(msgs []OpenAIChatMessage) string {
	for _, m := range msgs {
		if strings.EqualFold(m.Role, "user") {
			return strings.TrimSpace(FlattenContent(m.Content))
		}
	}
	return ""
}

// appendUserTurnPrompt renders one user-role turn into the upstream prompt and
// returns the real user instruction. OpenCode's <system-update> wrappers are
// stripped from the instruction and appended as compact system notices, so a
// chronologically replayed notice can never replace the task prompt.
func appendUserTurnPrompt(builder *strings.Builder, content string) string {
	userText, notices := splitOpenCodeSystemUpdates(content)
	if userText != "" {
		builder.WriteString("User: ")
		builder.WriteString(userText)
		builder.WriteString("\n")
	}
	for _, notice := range notices {
		if formatted := formatSystemNotice(notice); formatted != "" {
			builder.WriteString(formatted)
			builder.WriteString("\n")
		}
	}
	return userText
}

// HandleUnifiedChat handles /chat endpoint (text, images, screen recordings, ref videos)
func HandleUnifiedChat(c fiber.Ctx) error {
	input, err := GetChatService().DecodeUnifiedChat(c)
	if err != nil {
		if err == errPromptTooLarge {
			return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{"error": err.Error()})
		}
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	prompt := input.Prompt
	// reasoning_effort selects a thinking variant by name; unsupported families
	// keep the requested model untouched.
	reqModel := gemini.ApplyReasoningEffort(input.Model, input.ReasoningEffort)
	newChat := input.NewChat
	stream := input.Stream
	images := input.Images

	sessionID := GetChatService().SessionID(c, input.UserID)

	if newChat {
		UserSessions.Delete(sessionID)
	}

	_, model, requiredTier := GetChatService().ResolveModel(c, reqModel)
	pool := GetChatService().Pool()
	// Capture request-derived values before SendStreamWriter: the fiber
	// context is invalid once the handler returns and the stream writer
	// closure actually runs.
	streamUserIP := c.IP()
	streamHost := c.Host()
	requestDone := RequestDone(c)

	if stream && len(images) == 0 {
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		return c.SendStreamWriter(func(w *bufio.Writer) {
			// The handler's ctx is canceled when the handler returns (before
			// this closure runs), so streaming needs its own live context.
			streamCtx, streamCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer streamCancel()
			CancelWhenRequestDone(requestDone, streamCtx, streamCancel)

			var usedWorker *AccountWorker
			var streamResp *gemini.GeminiResponse
			startTime := time.Now()

			streamErr := pool.ExecuteQueuedStreamWithTier(streamCtx, sessionID, requiredTier, func(worker *AccountWorker) error {
				usedWorker = worker
				sr, err := worker.Client.AskStreamModel(prompt, model, func(chunk string) {
					data, _ := json.Marshal(fiber.Map{"text": chunk})
					fmt.Fprintf(w, "data: %s\n\n", data)
					w.Flush()
				})
				streamResp = sr
				return err
			})

			if streamErr != nil {
				data, _ := json.Marshal(fiber.Map{"error": streamErr.Error()})
				fmt.Fprintf(w, "data: %s\n\n", data)
				w.Flush()
				return
			}

			if streamResp != nil && usedWorker != nil {
				if len(streamResp.Images) > 0 {
					for i, imgURL := range streamResp.Images {
						filename := fmt.Sprintf("img_%s_%d.png", streamResp.ResponseID, i)
						if streamResp.ResponseID == "" {
							filename = fmt.Sprintf("img_%d_%d.png", time.Now().Unix(), i)
						}
						if err := DownloadAndClean(usedWorker.Client, imgURL, filename, "image"); err == nil {
							streamResp.Images[i] = fmt.Sprintf("http://%s/output/%s", streamHost, filename)
						}
					}
				}

				if len(streamResp.Videos) > 0 {
					vidURL := streamResp.Videos[0]
					if vidURL != "" && len(vidURL) > 50 {
						filename := fmt.Sprintf("vid_%s_0.mp4", streamResp.ResponseID)
						if streamResp.ResponseID == "" {
							filename = fmt.Sprintf("vid_%d_0.mp4", time.Now().Unix())
						}
						if err := DownloadAndClean(usedWorker.Client, vidURL, filename, "video"); err == nil {
							streamResp.Videos[0] = fmt.Sprintf("http://%s/output/%s", streamHost, filename)
						}
					}
				}
			}

			final, _ := json.Marshal(streamResp)
			fmt.Fprintf(w, "data: %s\n\n", final)
			fmt.Fprintf(w, "data: [DONE]\n\n")
			w.Flush()

			userIP := streamUserIP
			elapsed := time.Since(startTime).Seconds()
			tier, accID := "free", ""
			if usedWorker != nil {
				tier = usedWorker.Tier
				accID = usedWorker.ID
			}
			pTokens := CountTokensLimited(prompt)
			rTokens := 0
			convID := ""
			responseText := ""
			if streamResp != nil {
				responseText = streamResp.Text
				rTokens = CountTokensLimited(responseText)
				convID = streamResp.ConversationID
			}
			GetHistoryService().LogTurn(convID, sessionID, prompt, responseText, model, pTokens, rTokens, tier, accID)
			GetHistoryService().LogRequest("/chat", "POST", userIP, 200, elapsed*1000)
		})
	}

	if len(images) > 0 {
		log.Printf("📸 Image/Media request: %d items, prompt: %s (model: %s, tier: %s)", len(images), prompt, model, requiredTier)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var usedWorker *AccountWorker
	resp, err := pool.ExecuteQueuedWithTier(ctx, sessionID, requiredTier, func(w *AccountWorker) (*gemini.GeminiResponse, error) {
		usedWorker = w
		if len(images) > 0 {
			return w.Client.AskWithImagesModel(prompt, images, model)
		}
		return w.Client.AskModel(prompt, model)
	})

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			UserSessions.Delete(sessionID)
			return c.Status(504).JSON(fiber.Map{"error": "Request timed out. Session reset."})
		}
		if strings.Contains(strings.ToLower(err.Error()), "rate_limit") || strings.Contains(err.Error(), "429") || strings.Contains(strings.ToLower(err.Error()), "cooldown") {
			return c.Status(429).JSON(fiber.Map{
				"error": fiber.Map{
					"message": err.Error(),
					"type":    "rate_limit_error",
					"code":    429,
				},
			})
		}
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	if len(resp.Images) > 0 && usedWorker != nil {
		for i, imgURL := range resp.Images {
			filename := fmt.Sprintf("img_%s_%d.png", resp.ResponseID, i)
			if resp.ResponseID == "" {
				filename = fmt.Sprintf("img_%d_%d.png", time.Now().Unix(), i)
			}
			if err := DownloadAndClean(usedWorker.Client, imgURL, filename, "image"); err == nil {
				resp.Images[i] = fmt.Sprintf("http://%s/output/%s", c.Host(), filename)
			}
		}
	}

	if len(resp.Videos) > 0 && usedWorker != nil {
		vidURL := resp.Videos[0]
		if vidURL != "" && len(vidURL) > 50 {
			filename := fmt.Sprintf("vid_%s_0.mp4", resp.ResponseID)
			if resp.ResponseID == "" {
				filename = fmt.Sprintf("vid_%d_0.mp4", time.Now().Unix())
			}
			if err := DownloadAndClean(usedWorker.Client, vidURL, filename, "video"); err == nil {
				resp.Videos[0] = fmt.Sprintf("http://%s/output/%s", c.Host(), filename)
			}
		}
	}

	// Save to SQLite database asynchronously through the bounded history worker.
	userIP := c.IP()
	hostStr := c.Host()
	tier, accID := "free", ""
	if usedWorker != nil {
		tier = usedWorker.Tier
		accID = usedWorker.ID
	}
	pTokens := CountTokensLimited(prompt)
	rTokens := CountTokensLimited(resp.Text)
	history := GetHistoryService()
	history.LogTurn(resp.ConversationID, sessionID, prompt, resp.Text, model, pTokens, rTokens, tier, accID)
	for _, imgURL := range resp.Images {
		fName := filepath.Base(imgURL)
		history.LogMedia("image", resp.ConversationID, prompt, fName, "./output/"+fName, fmt.Sprintf("http://%s/output/%s", hostStr, fName), "16:9", resp.ResponseID)
	}
	for _, vidURL := range resp.Videos {
		fName := filepath.Base(vidURL)
		history.LogMedia("video", resp.ConversationID, prompt, fName, "./output/"+fName, fmt.Sprintf("http://%s/output/%s", hostStr, fName), "16:9", resp.ResponseID)
	}
	history.LogRequest("/chat", "POST", userIP, 200, resp.Elapsed*1000)

	return c.JSON(resp)
}

// recoveredToolResultContent is the placeholder injected when a tool call has
// no matching result. It is explicitly marked so the model can never mistake
// the hole for a successful (but empty) tool output and infer from it.
const recoveredToolResultContent = "[recovered] Tool response was lost during context processing. Do not assume this tool produced output."

// formatToolResultLabel builds the identifier of a flattened tool observation.
// The OpenAI transcript pairs a result to its call through tool_call_id, so the
// id is always serialized; the tool name is prepended when it adds information.
// A label is never empty: a harness that supplies neither field would otherwise
// produce the unparseable line "[Tool Result for ]:".
func formatToolResultLabel(name, toolCallID string) string {
	name = strings.TrimSpace(name)
	toolCallID = strings.TrimSpace(toolCallID)
	switch {
	case toolCallID == "":
		if name == "" {
			return "unknown_tool"
		}
		return name
	case name == "" || name == toolCallID:
		return toolCallID
	default:
		return fmt.Sprintf("%s (%s)", name, toolCallID)
	}
}

// reconcileTranscriptMessages enforces the assistant call → tool result
// bijection before the transcript is flattened into prose. A call left without
// a result (dropped by ThrottleMutatingToolCalls, or lost by a harness) would
// otherwise leave the model free to invent an output. Every unanswered call
// receives a visible, explicitly marked synthetic result placed right after
// the assistant message that emitted it. The input slice and its messages are
// never modified, real results keep their relative order, and a second pass is
// a fixed point: a synthesized result carries the call id, so the call is
// answered on the next run.
func reconcileTranscriptMessages(messages []OpenAIChatMessage) []OpenAIChatMessage {
	if len(messages) == 0 {
		return messages
	}

	// Pass 1: collect the results already present, keyed by tool_call_id with
	// an explicit name fallback for harnesses that omit the id.
	answered := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			continue
		}
		key := strings.TrimSpace(message.ToolCallID)
		if key == "" {
			key = strings.TrimSpace(message.Name)
		}
		if key != "" {
			answered[key] = struct{}{}
		}
	}

	// Pass 2: build a new list, appending one synthetic result per unanswered
	// call immediately after its assistant message.
	out := make([]OpenAIChatMessage, 0, len(messages))
	synthesized := false
	for _, message := range messages {
		out = append(out, message)
		if !strings.EqualFold(strings.TrimSpace(message.Role), "assistant") || len(message.ToolCalls) == 0 {
			continue
		}
		for _, call := range message.ToolCalls {
			id := strings.TrimSpace(call.ID)
			if id == "" {
				// An id-less call cannot be paired reliably; reconcile it by
				// name would risk inventing a result for a different call.
				continue
			}
			if _, exists := answered[id]; exists {
				continue
			}
			answered[id] = struct{}{}
			out = append(out, OpenAIChatMessage{
				Role:       "tool",
				ToolCallID: id,
				Name:       call.Function.Name,
				Content:    contentRawString(recoveredToolResultContent),
			})
			synthesized = true
		}
	}
	if !synthesized {
		return messages
	}
	return out
}

// streamBlockTracker projects cumulative upstream snapshots onto the deltas
// the client has not received yet. Unlike a monotone rune counter, it rebases
// its high-water mark whenever a snapshot stops extending the text already
// sent: a reopened thought block (or a restarted generation) is then emitted
// in full instead of being silently swallowed by the previous block's mark.
type streamBlockTracker struct {
	sent string
}

func (t *streamBlockTracker) reset() {
	t.sent = ""
}

// delta returns the portion of snapshot that has not been emitted yet.
func (t *streamBlockTracker) delta(snapshot string) string {
	if snapshot == "" || snapshot == t.sent {
		return ""
	}
	if strings.HasPrefix(snapshot, t.sent) {
		delta := snapshot[len(t.sent):]
		t.sent = snapshot
		return delta
	}
	if strings.HasPrefix(t.sent, snapshot) {
		// The snapshot retracted into the already-emitted prefix (a partial
		// tag was re-interpreted): nothing new, and the high-water mark must
		// not move backwards or the retracted text would be sent twice.
		return ""
	}
	// The snapshot no longer extends what was sent: a new thought block was
	// opened (or the generation restarted). Keep the longest common prefix so
	// the new block is emitted from its own start without replaying shared
	// text, then rebase the high-water mark on the new snapshot.
	previous := []rune(t.sent)
	current := []rune(snapshot)
	common := 0
	for common < len(previous) && common < len(current) && previous[common] == current[common] {
		common++
	}
	t.sent = snapshot
	return string(current[common:])
}

// HandleOpenAIChatCompletions handles /v1/chat/completions with Native Tool Calling & Output Arranger
func HandleOpenAIChatCompletions(c fiber.Ctx) error {
	var req OpenAIChatCompletionRequest
	if err := c.Bind().JSON(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
	}

	bodyStr := string(c.Body())
	hasToolsInRaw := strings.Contains(bodyStr, "\"tools\"")
	log.Printf("🔥 [DEBUG TOOLS] Model=%s msgs=%d tools=%d hasToolsInRaw=%v", req.Model, len(req.Messages), len(req.Tools), hasToolsInRaw)
	if len(req.Tools) > 0 {
		var toolNames []string
		for _, t := range req.Tools {
			if fn, ok := t["function"].(map[string]any); ok {
				if n, ok := fn["name"].(string); ok {
					toolNames = append(toolNames, n)
				}
			}
		}
		log.Printf("🔥 [DEBUG TOOLS] Tool names: %v", toolNames)
	}

	if len(req.Messages) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "Messages array is empty"})
	}

	// reasoning_effort selects a registry variant by name instead of mutating
	// the shared ModelConfig (whose ThinkingLevel is read concurrently by every
	// in-flight request through BuildHeaderValue). An unsupported family keeps
	// the requested model unchanged.
	requestedModel := gemini.ApplyReasoningEffort(req.Model, req.ReasoningEffort)
	if requestedModel != req.Model {
		log.Printf("🧠 reasoning_effort=%q maps model %q -> %q", req.ReasoningEffort, req.Model, requestedModel)
	}
	_, model, requiredTier := GetChatService().ResolveModelForRequest(c, requestedModel, len(req.Tools) > 0)

	var tiBlock string
	if len(req.Tools) > 0 {
		tiBlock = gemini.FormatToolsInstruction(req.Tools)
	}

	var prompt string
	var fullPromptBuilder strings.Builder

	// Separate initial system prompt messages from conversation history.
	// Only messages before the first non-system message are treated as initial system directives.
	// Any system message appearing later in the conversation (e.g. dynamic notices from harnesses
	// when tools change) is kept in chronological order as an inline notice, so the prefix of
	// the conversation transcript is preserved across multi-turn tool interactions.
	var systemContent string
	var nonSystemMessages []OpenAIChatMessage
	inInitialSystem := true
	for _, m := range req.Messages {
		if inInitialSystem && strings.EqualFold(m.Role, "system") {
			sc := FlattenContent(m.Content)
			if compact, ok := compactSystemNotice(sc); ok {
				// A replayed OpenCode notice is not the harness's authoritative
				// system prompt; keep only its neutral compact form.
				sc = compact
			}
			if systemContent == "" {
				systemContent = sc
			} else {
				systemContent += "\n\n" + sc
			}
		} else {
			inInitialSystem = false
			nonSystemMessages = append(nonSystemMessages, m)
		}
	}

	// Keep the unmasked transcript for the evidence ledger: masking may
	// compact an old tool result past an error marker, and a synthetic
	// [recovered] result is never evidence of a successful mutation.
	pristineNonSystemMessages := nonSystemMessages

	// Block Observation Masking: once a replay would consume more than 60% of
	// the prompt budget, the transcript contains more than six tool
	// observations, or the chat exceeds twelve messages, compact old tool
	// observations while keeping the four most recent ones intact. This runs
	// before flattening so stale logs are never replayed byte-for-byte, and it
	// keeps the 1097 replay (one user message followed by a long assistant/tool
	// chain) compact enough for the upstream backend.
	nonSystemMessages = MaskOldToolObservationsWithOverhead(
		nonSystemMessages,
		defaultObservationMaskKeepRecent,
		defaultObservationMaskThreshold,
		len(systemContent)+len(tiBlock),
	)

	// Enforce the call↔result bijection before flattening: a call without a
	// result (throttled mutation, aborted turn) receives an explicit
	// "[recovered]" placeholder instead of silently dangling. The pristine
	// list is kept for the prompt fallback so a placeholder can never become
	// the task itself.
	originalNonSystemMessages := nonSystemMessages
	nonSystemMessages = reconcileTranscriptMessages(nonSystemMessages)

	// Write System directives & tool schemas at the very top of the prompt
	if systemContent != "" {
		fullPromptBuilder.WriteString("[System]: ")
		fullPromptBuilder.WriteString(systemContent)
		fullPromptBuilder.WriteString("\n\n")
	}
	if tiBlock != "" {
		fullPromptBuilder.WriteString(tiBlock)
		fullPromptBuilder.WriteString("\n\n")
	}

	for _, m := range nonSystemMessages {
		contentStr := FlattenContent(m.Content)
		if strings.EqualFold(m.Role, "user") {
			// OpenCode 2.4.0 downgrades chronological system messages to the
			// user role and wraps them in <system-update>...</system-update>.
			// Treat that payload as a notice, never as the user's task: the
			// real instruction may precede the wrapper in the same message.
			if userText := appendUserTurnPrompt(&fullPromptBuilder, contentStr); userText != "" {
				prompt = userText
			}
		} else if strings.EqualFold(m.Role, "assistant") {
			if len(m.ToolCalls) > 0 {
				tcJSON, _ := json.Marshal(m.ToolCalls)
				fullPromptBuilder.WriteString("Assistant called tools: ")
				fullPromptBuilder.Write(tcJSON)
				fullPromptBuilder.WriteString("\n")
			} else if contentStr != "" {
				fullPromptBuilder.WriteString("Assistant: ")
				fullPromptBuilder.WriteString(contentStr)
				fullPromptBuilder.WriteString("\n")
			}
		} else if strings.EqualFold(m.Role, "tool") {
			label := formatToolResultLabel(m.Name, m.ToolCallID)
			fullPromptBuilder.WriteString(fmt.Sprintf("[Tool Result for %s]: %s\n", label, contentStr))
		} else if strings.EqualFold(m.Role, "system") {
			if notice := formatSystemNotice(contentStr); notice != "" {
				fullPromptBuilder.WriteString(notice)
				fullPromptBuilder.WriteString("\n")
			}
		}
	}

	if prompt == "" && len(originalNonSystemMessages) > 0 {
		lastM := originalNonSystemMessages[len(originalNonSystemMessages)-1]
		fallback := FlattenContent(lastM.Content)
		if strings.EqualFold(lastM.Role, "user") {
			// Never fall back to an OpenCode notice-only wrapper: if the
			// conversation carries no real user instruction, leave `prompt`
			// empty rather than logging/injecting the notice as the task.
			fallback, _ = splitOpenCodeSystemUpdates(fallback)
		}
		prompt = fallback
	}

	// Multi-agent & session affinity resolution
	explicitSession := true
	sessionID := strings.TrimSpace(c.Get("X-Agent-ID"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(c.Get("X-Session-ID"))
	}
	if sessionID == "" {
		sessionID = strings.TrimSpace(c.Get("X-Conversation-ID"))
	}
	if sessionID == "" && req.User != nil {
		sessionID = strings.TrimSpace(*req.User)
	}
	if sessionID == "" {
		sessionID = strings.TrimSpace(c.Query("session_id"))
	}
	if sessionID == "" {
		explicitSession = false
		sessionID = c.IP()
	}
	sessionID = sanitizeSessionID(sessionID, c.IP())

	// Clients that send no affinity identifier (OpenCode / AI-SDK) previously
	// collapsed every chat behind the same IP into a single shared upstream
	// conversation, so a bad turn or refusal in one chat leaked into every
	// other one. Scope the fallback by the conversation's opening user
	// message, which stays stable across all turns of one chat (harnesses
	// replay the full transcript each turn) and differs between chats.
	if !explicitSession {
		if first := firstUserMessageContent(req.Messages); first != "" {
			sessionID += "|chat:" + contentScopeKey(first)
		}
	}

	// Scope the continuation bookkeeping by system-prompt identity: harnesses
	// fire auxiliary one-off requests (session titling, summarizers) with a
	// completely different system prompt through the same client IP, and they
	// must not reset or pollute the main chat thread's upstream conversation.
	sessionKey := sessionID
	if len(req.Messages) > 0 && strings.EqualFold(req.Messages[0].Role, "system") {
		if scope := systemScopeKey(FlattenContent(req.Messages[0].Content)); scope != "" {
			sessionKey = sessionID + "|" + scope
		}
	}

	// Rebuild the evidence ledger from the pristine transcript before any
	// masking or reconciliation can drop old tool results. Only a successful
	// tool result can back a later completion claim.
	recordSessionLedgerFromMessages(sessionKey, pristineNonSystemMessages)

	// Stateful doom-loop gateway: inspect the session's recent emitted calls
	// before spending an upstream turn. A fresh task (only system/user
	// messages) resets the tracker. Repeated or rotating calls trigger a
	// maximum-recency warning; five identical calls (or three full cycles)
	// trip a hard circuit breaker.
	tracker := getOrCreateSessionTracker(sessionKey)
	if requestStartsFreshTask(req.Messages) || requestStartsNewUserTurn(req.Messages) {
		tracker.Reset()
	}
	loopAction, loopDetail := tracker.CheckIntervention()
	antiLoopDirective := ""
	switch loopAction {
	case ActionHalt:
		RecordDoomLoopIntervention(doomLoopSignal(loopAction, loopDetail))
		invalidateSessionTurn(sessionKey)
		humanDetail := doomLoopHumanDetail(loopDetail)
		log.Printf("🛑 Doom-loop circuit breaker for session %q: %s", sessionKey, humanDetail)
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"error": fiber.Map{
				"message": fmt.Sprintf("doom-loop detected: %s", humanDetail),
				"type":    "doom_loop_error",
				"code":    fiber.StatusTooManyRequests,
			},
		})
	case ActionWarnRepeat, ActionWarnPassiveRead:
		RecordDoomLoopIntervention(doomLoopSignal(loopAction, loopDetail))
		antiLoopDirective = formatDoomLoopDirective(loopAction, loopDetail)
		log.Printf("⚠️ Doom-loop warning for session %q: %s", sessionKey, loopDetail)
	}

	// Counter-S7 ground truth: when the client replays a tool result that
	// reports a failure, inject an explicit recovery directive at maximum
	// recency. This makes it much harder for the model to ignore the failure
	// and declare a false success on the next turn.
	if groundTruth := formatRecentToolErrorDirective(nonSystemMessages); groundTruth != "" {
		RecordGroundTruthDirective()
		antiLoopDirective += groundTruth
		log.Printf("🧪 Ground-truth recovery directive injected for session %q", sessionKey)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	turnCue := "\nAssistant: "
	if len(nonSystemMessages) > 0 {
		lastM := nonSystemMessages[len(nonSystemMessages)-1]
		if lastM.Role == "assistant" {
			turnCue = ""
		}
	}
	effectiveTurnCue := turnCue
	if antiLoopDirective != "" {
		// A role:user directive must be followed by a fresh assistant turn.
		effectiveTurnCue = "\nAssistant: "
	}
	msgFlat := fullPromptBuilder.String()
	if len(msgFlat) > maxPromptBytes || len(prompt) > maxPromptBytes {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{"error": "prompt exceeds 1 MiB limit"})
	}

	pool := GetWorkerPool()
	streamUserIP := c.IP() // capture before SendStreamWriter: the request ctx is invalid inside the closure
	requestDone := RequestDone(c)
	if req.Stream {
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		return c.SendStreamWriter(func(w *bufio.Writer) {
			var writeMu sync.Mutex
			lastWrite := time.Now()
			writeChunk := func(data []byte) {
				writeMu.Lock()
				defer writeMu.Unlock()
				lastWrite = time.Now()
				fmt.Fprintf(w, "data: %s\n\n", data)
				_ = w.Flush()
			}
			writeRaw := func(format string, args ...any) {
				writeMu.Lock()
				defer writeMu.Unlock()
				lastWrite = time.Now()
				fmt.Fprintf(w, format, args...)
				_ = w.Flush()
			}

			// Immediately flush response headers + SSE comment to client.
			// This confirms HTTP 200 OK within <1ms and completely prevents client-side
			// response-headers timeouts (e.g. OpenCode 5-min limit).
			writeRaw(": connected\n\n")

			// The handler's ctx is canceled when the handler returns (before this
			// closure runs), so streaming needs its own live context.
			streamCtx, streamCancel := context.WithTimeout(context.Background(), StreamTimeout())
			defer streamCancel()
			CancelWhenRequestDone(requestDone, streamCtx, streamCancel)

			// Launch periodic SSE keep-alive ping to prevent idle connection drops
			heartbeatStop := make(chan struct{})
			defer close(heartbeatStop)

			go func() {
				ticker := time.NewTicker(15 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-heartbeatStop:
						return
					case <-streamCtx.Done():
						return
					case <-ticker.C:
						writeMu.Lock()
						if time.Since(lastWrite) >= 14*time.Second {
							fmt.Fprintf(w, ": ping\n\n")
							_ = w.Flush()
						}
						writeMu.Unlock()
					}
				}
			}()

			chunkID := "chatcmpl-" + uuid.NewString()[:12]
			createdTime := time.Now().Unix()
			var usedWorker *AccountWorker
			var streamResp *gemini.GeminiResponse
			var accumulatedOutput strings.Builder
			var thoughtTracker streamBlockTracker
			var contentTracker streamBlockTracker
			var lastSendPromptLen, lastDeltaLen int
			var replayReason string
			emitSummary := func(class, finish string, visibleLen int, convID string) {
				emitTurnSummary(sessionKey, model, lastSendPromptLen, lastDeltaLen, replayReason, convID, visibleLen, len(req.Tools), class, finish)
			}

			streamErr := pool.ExecuteQueuedStreamWithTier(streamCtx, sessionID, requiredTier, func(wWorker *AccountWorker) error {
				usedWorker = wWorker
				// With tools requested, chunks are buffered (never streamed to
				// the client until arranged), so a blocked/empty generation can
				// be retried invisibly on a fresh upstream conversation.
				// Transport incidents use a dedicated budget and never consume
				// a functional attempt.
				maxAttempts := maxChatAttempts(true, len(req.Tools) > 0)
				forceFull := false
				schemaRetryDetail := ""
				truncationRetry := false
				pseudoReportRetry := false
				unbackedRetry := false
				transportRetries := 0
				emptyGenerationRetries := 0
				// sendModel is the upstream model actually queried. It starts as
				// the client-facing model and only escalates to a deeper thinking
				// variant after a technically empty turn; the response keeps
				// reporting the model the caller asked for.
				sendModel := model
				for attempt := 0; attempt < maxAttempts; attempt++ {
					var sendPrompt string
					thoughtTracker.reset()
					contentTracker.reset()
					if forceFull {
						// Previous attempt hit a blocked/empty generation, a
						// refusal, or an invalid tool call: replay everything
						// fresh with a targeted override directive.
						replayReason = "forced"
						lastDeltaLen = 0
						RecordDeltaReplay("forced")
						wWorker.Client.ResetConversationStateSession(sessionKey, sendModel)
						override := ""
						if len(req.Tools) > 0 {
							switch {
							case pseudoReportRetry:
								override = formatPseudoReportRetryDirective()
							case unbackedRetry:
								override = formatUnbackedCompletionRetryDirective()
							case truncationRetry:
								override = formatTruncationRetryDirective()
							case schemaRetryDetail != "":
								override = formatSchemaRetryDirective(schemaRetryDetail)
							default:
								override = unlimitedToolExecutionDirective
							}
						}
						sendPrompt = override + msgFlat + antiLoopDirective + effectiveTurnCue
					} else {
						// Continuation strategy: continue the session's live upstream
						// conversation with just the new transcript delta, or reset and
						// replay the full transcript when that is not safe.
						curConv := wWorker.Client.CurrentConversationIDFor(sessionKey, sendModel)
						plan := planSessionTurnWithReason(sessionKey, wWorker.ID, curConv, msgFlat)
						if plan.needReset {
							// The worker may have been recreated by a cookie re-sync
							// (pool reload), dropping client-side conversation state
							// while the upstream conversation still exists. Restore
							// the recorded pointer when this worker owns it.
							if conv, respID, choiceID, lastWorker, valid := peekSessionTurnConv(sessionKey); valid && conv != "" && lastWorker == wWorker.ID && curConv == "" {
								wWorker.Client.RestoreConversationStateSession(sessionKey, sendModel, conv, respID, choiceID)
								plan = planSessionTurnWithReason(sessionKey, wWorker.ID, conv, msgFlat)
							}
						}
						replayReason = plan.reason
						lastDeltaLen = plan.deltaBytes
						if plan.needReset {
							RecordDeltaReplay(plan.reason)
							// Full replay: message transcript, turn cue, tool schemas.
							wWorker.Client.ResetConversationStateSession(sessionKey, sendModel)
							sendPrompt = msgFlat + antiLoopDirective + effectiveTurnCue
						} else {
							sendPrompt = plan.prompt + antiLoopDirective + effectiveTurnCue
						}
					}
					lastSendPromptLen = len(sendPrompt)

					accumulatedOutput.Reset()
					sr, err := wWorker.Client.AskStreamModelSession(sendPrompt, sendModel, func(chunk string) {
						accumulatedOutput.WriteString(chunk)

						rawSoFar := accumulatedOutput.String()

						// 1. Live stream thinking tokens whenever available. The
						// tracker rebases on a reopened thought block instead of
						// comparing against a stale monotone high-water mark.
						allThoughts := gemini.ExtractAvailableThoughts(rawSoFar)
						if deltaThought := thoughtTracker.delta(allThoughts); deltaThought != "" {
							chunkResp := OpenAIChatCompletionChunk{
								ID:      chunkID,
								Object:  "chat.completion.chunk",
								Created: createdTime,
								Model:   model,
								Choices: []OpenAIStreamChoice{
									{
										Index: 0,
										Delta: OpenAIDelta{
											Role:             "assistant",
											ReasoningContent: deltaThought,
										},
									},
								},
							}
							data, _ := json.Marshal(chunkResp)
							writeChunk(data)
						}

						if len(req.Tools) > 0 {
							// Buffer chunks when tools are requested to evaluate tool call
							return
						}

						// 2. For non-tool requests, stream content (outside thoughts)
						allContent := gemini.ExtractAvailableContent(rawSoFar)
						if deltaContent := contentTracker.delta(allContent); deltaContent != "" {
							chunkResp := OpenAIChatCompletionChunk{
								ID:      chunkID,
								Object:  "chat.completion.chunk",
								Created: createdTime,
								Model:   model,
								Choices: []OpenAIStreamChoice{
									{
										Index: 0,
										Delta: OpenAIDelta{
											Role:    "assistant",
											Content: deltaContent,
										},
									},
								},
							}
							data, _ := json.Marshal(chunkResp)
							writeChunk(data)
						}
					}, sessionKey)
					streamResp = sr
					if err != nil {
						if shouldRetryTransport(err, transportRetries) {
							// Transport/session incident: reset the upstream
							// conversation and retry on a fresh one without
							// consuming the model's functional attempt.
							transportRetries++
							log.Printf("⚠️ Stream transport incident %d/%d (%v), retrying on a fresh upstream conversation without consuming a generation attempt", transportRetries, maxTransportRetries, err)
							wWorker.Client.ResetConversationStateSession(sessionKey, sendModel)
							truncationRetry = false
							schemaRetryDetail = ""
							pseudoReportRetry = false
							unbackedRetry = false
							invalidateSessionTurn(sessionKey)
							forceFull = true
							// The loop's post statement re-increments attempt, so
							// this replay keeps the same functional slot.
							attempt--
							continue
						}
						if attempt+1 < maxAttempts {
							log.Printf("⚠️ Stream attempt %d failed (%v), retrying on a fresh upstream conversation", attempt+1, err)
							truncationRetry = false
							schemaRetryDetail = ""
							pseudoReportRetry = false
							unbackedRetry = false
							invalidateSessionTurn(sessionKey)
							forceFull = true
							continue
						}
						return err
					}
					// Retry on a blocked/empty generation: the poisoned upstream
					// conversation is reset and the full transcript (with tool
					// schemas) is replayed fresh.
					finalAttemptText := accumulatedOutput.String()
					if streamResp != nil && len(streamResp.Text) > len(finalAttemptText) {
						finalAttemptText = streamResp.Text
					}
					// An aborted stream or a thought-only turn executed nothing.
					// Charge it to the dedicated technical budget instead of a
					// functional attempt meant for pseudo-reports, refusals,
					// schema errors, or truncations.
					if len(req.Tools) > 0 && isEmptyGenerationOutput(finalAttemptText, req.Tools) && emptyGenerationRetries < maxEmptyGenerationRetries {
						recordTurnClass("incomplete", model)
						emptyGenerationRetries++
						// An identical empty turn replayed at the same depth
						// tends to fail identically. Escalate to the next
						// thinking variant (Low -> Medium -> High, then a fixed
						// point) by model name, never by mutating the shared
						// registry entry.
						if escalated := gemini.EscalateThinkingModel(sendModel); escalated != sendModel {
							wWorker.Client.ResetConversationStateSession(sessionKey, sendModel)
							log.Printf("⚠️ Empty/thought-only generation %d/%d: escalating thinking model %q -> %q", emptyGenerationRetries, maxEmptyGenerationRetries, sendModel, escalated)
							sendModel = escalated
						} else {
							log.Printf("⚠️ Empty/thought-only generation %d/%d, retrying on a fresh upstream conversation without consuming a generation attempt", emptyGenerationRetries, maxEmptyGenerationRetries)
						}
						truncationRetry = false
						schemaRetryDetail = ""
						pseudoReportRetry = false
						unbackedRetry = false
						invalidateSessionTurn(sessionKey)
						forceFull = true
						// The loop's post statement re-increments attempt, so
						// this replay keeps the same functional slot.
						attempt--
						continue
					}
					if attempt+1 < maxAttempts {
						if gemini.HasTruncatedMutationToolCall(finalAttemptText) {
							// The model was cut off inside a file mutation. Never
							// repair or execute the partial payload: reset the
							// upstream conversation and retry with a
							// truncation-specific abstract directive.
							RecordArrangerRejection("mid_value_truncation")
							RecordQuarantine("truncated")
							recordTurnClass("untagged_mutation", model)
							log.Printf("⚠️ Truncated mutation tool call detected, retrying once with an abstract truncation directive")
							truncationRetry = true
							schemaRetryDetail = ""
							pseudoReportRetry = false
							unbackedRetry = false
							invalidateSessionTurn(sessionKey)
							forceFull = true
							continue
						}
						if invalid, detail := gemini.HasInvalidSchemaToolCall(finalAttemptText, req.Tools); invalid {
							RecordArrangerRejection(arrangerRejectionReason(detail))
							RecordQuarantine("schema_invalid")
							recordTurnClass("schema_invalid", model)
							log.Printf("⚠️ Invalid tool-call schema (%s), retrying with an abstract error directive", detail)
							schemaRetryDetail = detail
							truncationRetry = false
							pseudoReportRetry = false
							unbackedRetry = false
							invalidateSessionTurn(sessionKey)
							forceFull = true
							continue
						}
						if len(req.Tools) > 0 && gemini.ArrangeGeminiOutputWithTools(finalAttemptText, req.Tools).UntaggedMutation {
							RecordArrangerRejection("untagged_mutation")
							RecordQuarantine("untagged_mutation")
							recordTurnClass("untagged_mutation", model)
							log.Printf("⚠️ Untagged mutating tool JSON detected in prose, retrying with truncation directive")
							truncationRetry = true
							schemaRetryDetail = ""
							pseudoReportRetry = false
							unbackedRetry = false
							invalidateSessionTurn(sessionKey)
							forceFull = true
							continue
						}
						if len(req.Tools) > 0 && isUnbackedCompletion(finalAttemptText, req.Tools, sessionKey) {
							RecordArrangerRejection("unbacked_completion")
							recordTurnClass("unbacked_completion", model)
							log.Printf("⚠️ Unbacked completion claim detected (S7), retrying with ground-truth directive")
							pseudoReportRetry = false
							truncationRetry = false
							schemaRetryDetail = ""
							unbackedRetry = true
							invalidateSessionTurn(sessionKey)
							forceFull = true
							continue
						}
						if isUnusableModelOutput(finalAttemptText, req.Tools) {
							class := classifyTurnOutput(finalAttemptText, gemini.ArrangeGeminiOutputWithTools(finalAttemptText, req.Tools), req.Tools, sessionKey)
							recordTurnClass(class, model)
							if len(req.Tools) > 0 && isPseudoProgressReport(finalAttemptText, req.Tools) {
								log.Printf("⚠️ Pseudo-report without a tool call (%d chars), retrying with an anti-summary directive", len(finalAttemptText))
								pseudoReportRetry = true
								truncationRetry = false
								schemaRetryDetail = ""
								unbackedRetry = false
							} else {
								if len(req.Tools) > 0 && isToolRefusalOutput(finalAttemptText, req.Tools) {
									RecordRefusalRetry()
								}
								truncationRetry = false
								schemaRetryDetail = ""
								pseudoReportRetry = false
								unbackedRetry = false
								log.Printf("⚠️ Unusable model output (%d chars), retrying on a fresh upstream conversation", len(finalAttemptText))
							}
							invalidateSessionTurn(sessionKey)
							forceFull = true
							continue
						}
					}
					return nil
				}
				return nil
			})

			if streamErr != nil {
				log.Printf("❌ Streaming error: %v", streamErr)
				invalidateSessionTurn(sessionKey)
				emitSummary("none", "error", 0, "")
				errChunk := newStreamErrorChunk(chunkID, model, createdTime, fmt.Sprintf("\n\n[Error: %v]", streamErr))
				if errBytes, mErr := json.Marshal(errChunk); mErr == nil {
					writeChunk(errBytes)
				}
				writeRaw("data: [DONE]\n\n")
				return
			}

			// Commit the turn only if the model produced usable output; a
			// blocked/empty generation invalidates the session conversation so
			// the next request resets it and replays the full transcript.
			finalText := accumulatedOutput.String()
			if streamResp != nil && len(streamResp.Text) > len(finalText) {
				finalText = streamResp.Text
			}
			if len(req.Tools) > 0 {
				if invalid, detail := gemini.HasInvalidSchemaToolCall(finalText, req.Tools); invalid {
					RecordArrangerRejection(arrangerRejectionReason(detail))
					RecordQuarantine("schema_invalid")
					recordTurnClass("schema_invalid", model)
					log.Printf("❌ Upstream tool call failed schema validation after retry: %s", detail)
					invalidateSessionTurn(sessionKey)
					convID := ""
					if streamResp != nil {
						convID = streamResp.ConversationID
					}
					emitSummary("schema_invalid", "error", len(finalText), convID)
					errChunk := newStreamErrorChunk(chunkID, model, createdTime, fmt.Sprintf("\n\n[Error: upstream tool call failed schema validation: %s]", detail))
					if errBytes, mErr := json.Marshal(errChunk); mErr == nil {
						writeChunk(errBytes)
					}
					writeRaw("data: [DONE]\n\n")
					return
				}
			}
			if isUnusableModelOutput(finalText, req.Tools) || isUnbackedCompletion(finalText, req.Tools, sessionKey) {
				invalidateSessionTurn(sessionKey)
			} else if streamResp != nil {
				lastWorkerID := ""
				if usedWorker != nil {
					lastWorkerID = usedWorker.ID
				}
				commitSessionTurn(sessionKey, msgFlat, streamResp.ConversationID, streamResp.ResponseID, streamResp.ChoiceID, lastWorkerID)
			}

			// If tools were provided, arrange and emit the structured result
			finishReason := "stop"
			var finalContentForLog string
			var turnArranged *gemini.ArrangedOutput
			if len(req.Tools) > 0 {
				rawOutput := accumulatedOutput.String()
				if streamResp != nil && streamResp.Text != "" && len(rawOutput) < len(streamResp.Text) {
					rawOutput = streamResp.Text
				}
				arranged := gemini.ArrangeGeminiOutputWithTools(rawOutput, req.Tools)
				turnArranged = arranged
				recordArrangerRepairs(arranged)
				if arranged.IsToolCall {
					arranged.ToolCalls = gemini.ThrottleMutatingToolCalls(arranged.ToolCalls)
					if len(arranged.ToolCalls) == 0 {
						arranged.IsToolCall = false
						arranged.FinishReason = "stop"
					}
				}
				if gemini.HasTruncatedMutationToolCall(rawOutput) {
					// The model was cut off inside a mutating tool call. The
					// arranger deliberately rejected it; report an explicit
					// error instead of silently returning a stop response.
					RecordArrangerRejection("mid_value_truncation")
					RecordQuarantine("truncated")
					recordTurnClass("untagged_mutation", model)
					convID := ""
					if streamResp != nil {
						convID = streamResp.ConversationID
					}
					emitSummary("untagged_mutation", "error", len(arranged.CleanText), convID)
					errChunk := newStreamErrorChunk(chunkID, model, createdTime, "\n\n[Error: upstream tool call was truncated mid-value; no mutation was executed. Please retry.]")
					if errBytes, mErr := json.Marshal(errChunk); mErr == nil {
						writeChunk(errBytes)
					}
					writeRaw("data: [DONE]\n\n")
					return
				}
				finishReason = arranged.FinishReason
				finalContentForLog = arranged.CleanText

				// A tool-bearing turn that ends with neither a tool call nor
				// usable visible content would look like a successful stop to
				// the host and silently end the task. This barrier also covers
				// a pseudo-report, a refusal, a quarantined untagged mutation,
				// or an unbacked completion claim that survived the retry
				// budget: none may be returned as a successful stop.
				if shouldBlockUnusableToolTurn(arranged, finalText, req.Tools) ||
					isUnbackedCompletion(finalText, req.Tools, sessionKey) {
					RecordArrangerRejection("unusable_action_with_tools")
					class := classifyTurnOutput(finalText, arranged, req.Tools, sessionKey)
					recordTurnClass(class, model)
					if class == "untagged_mutation" {
						RecordQuarantine("untagged_mutation")
					}
					invalidateSessionTurn(sessionKey)
					convID := ""
					if streamResp != nil {
						convID = streamResp.ConversationID
					}
					emitSummary(class, "error", len(arranged.CleanText), convID)
					errChunk := newStreamErrorChunk(chunkID, model, createdTime, "\n\n[Error: "+unusableActionErrorMessage+". Please retry.]")
					if errBytes, mErr := json.Marshal(errChunk); mErr == nil {
						writeChunk(errBytes)
					}
					writeRaw("data: [DONE]\n\n")
					return
				}
				if arranged.IsToolCall {
					recordSessionToolCalls(sessionKey, arranged.ToolCalls)
					recordSessionLedgerToolCalls(sessionKey, arranged.ToolCalls)
				}

				// Emit any remaining reasoning tokens not yet streamed. The
				// tracker handles a final snapshot that extends, rewrites, or
				// reopens what was streamed live.
				if deltaThought := thoughtTracker.delta(arranged.ReasoningContent); deltaThought != "" {
					chunkResp := OpenAIChatCompletionChunk{
						ID:      chunkID,
						Object:  "chat.completion.chunk",
						Created: createdTime,
						Model:   model,
						Choices: []OpenAIStreamChoice{
							{
								Index: 0,
								Delta: OpenAIDelta{
									Role:             "assistant",
									ReasoningContent: deltaThought,
								},
							},
						},
					}
					data, _ := json.Marshal(chunkResp)
					writeChunk(data)
				}

				if arranged.IsToolCall {
					if arranged.CleanText != "" {
						chunkResp := OpenAIChatCompletionChunk{
							ID:      chunkID,
							Object:  "chat.completion.chunk",
							Created: createdTime,
							Model:   model,
							Choices: []OpenAIStreamChoice{
								{
									Index: 0,
									Delta: OpenAIDelta{
										Role:    "assistant",
										Content: arranged.CleanText,
									},
								},
							},
						}
						data, _ := json.Marshal(chunkResp)
						writeChunk(data)
					}

					var toolCalls []OpenAIToolCall
					for i, tc := range arranged.ToolCalls {
						idx := i
						toolCalls = append(toolCalls, OpenAIToolCall{
							Index: &idx,
							ID:    tc.ID,
							Type:  tc.Type,
							Function: OpenAIToolCallFunction{
								Name:      tc.Function.Name,
								Arguments: tc.Function.Arguments,
							},
						})
					}
					chunkResp := OpenAIChatCompletionChunk{
						ID:      chunkID,
						Object:  "chat.completion.chunk",
						Created: createdTime,
						Model:   model,
						Choices: []OpenAIStreamChoice{
							{
								Index: 0,
								Delta: OpenAIDelta{
									Role:      "assistant",
									ToolCalls: toolCalls,
								},
							},
						},
					}
					data, _ := json.Marshal(chunkResp)
					writeChunk(data)
				} else if arranged.CleanText != "" {
					chunkResp := OpenAIChatCompletionChunk{
						ID:      chunkID,
						Object:  "chat.completion.chunk",
						Created: createdTime,
						Model:   model,
						Choices: []OpenAIStreamChoice{
							{
								Index: 0,
								Delta: OpenAIDelta{
									Role:    "assistant",
									Content: arranged.CleanText,
								},
							},
						},
					}
					data, _ := json.Marshal(chunkResp)
					writeChunk(data)
				}
			} else if streamResp != nil {
				finalContentForLog = streamResp.Text
			}

			finalResp := OpenAIChatCompletionChunk{
				ID:      chunkID,
				Object:  "chat.completion.chunk",
				Created: createdTime,
				Model:   model,
				Choices: []OpenAIStreamChoice{
					{
						Index:        0,
						FinishReason: finishReason,
					},
				},
			}
			data, _ := json.Marshal(finalResp)
			writeChunk(data)
			writeRaw("data: [DONE]\n\n")

			userIP := streamUserIP
			tier, accID := "free", ""
			if usedWorker != nil {
				tier = usedWorker.Tier
				accID = usedWorker.ID
			}
			pTokens := CountTokensLimited(prompt)
			rTokens := CountTokensLimited(finalContentForLog)
			convID := ""
			if streamResp != nil {
				convID = streamResp.ConversationID
			}
			class := classifyTurnOutput(finalText, turnArranged, req.Tools, sessionKey)
			recordTurnClass(class, model)
			emitSummary(class, finishReason, len(finalContentForLog), convID)
			history := GetHistoryService()
			history.LogTurn(convID, sessionID, prompt, finalContentForLog, model, pTokens, rTokens, tier, accID)
			history.LogRequest("/v1/chat/completions", "POST", userIP, 200, float64(time.Now().Unix()-createdTime)*1000)
		})
	}

	var usedWorker *AccountWorker
	var lastSendPromptLen, lastDeltaLen int
	var replayReason string
	resp, err := pool.ExecuteQueuedWithTier(ctx, sessionID, requiredTier, func(w *AccountWorker) (*gemini.GeminiResponse, error) {
		usedWorker = w
		// Nothing has been emitted yet, so a blocked/empty generation can be
		// retried invisibly on a fresh upstream conversation. Transport
		// incidents use a dedicated budget and never consume a functional
		// generation attempt.
		maxAttempts := maxChatAttempts(false, len(req.Tools) > 0)
		forceFull := false
		schemaRetryDetail := ""
		truncationRetry := false
		pseudoReportRetry := false
		unbackedRetry := false
		transportRetries := 0
		emptyGenerationRetries := 0
		// sendModel is the upstream model actually queried; it escalates to a
		// deeper thinking variant after a technically empty turn. The response
		// still reports the model the caller requested.
		sendModel := model
		for attempt := 0; attempt < maxAttempts; attempt++ {
			var sendPrompt string
			if forceFull {
				// Previous attempt hit a blocked/empty generation, a refusal,
				// or an invalid tool call: replay everything fresh with a
				// targeted override directive.
				replayReason = "forced"
				lastDeltaLen = 0
				RecordDeltaReplay("forced")
				w.Client.ResetConversationStateSession(sessionKey, sendModel)
				override := ""
				if len(req.Tools) > 0 {
					switch {
					case pseudoReportRetry:
						override = formatPseudoReportRetryDirective()
					case unbackedRetry:
						override = formatUnbackedCompletionRetryDirective()
					case truncationRetry:
						override = formatTruncationRetryDirective()
					case schemaRetryDetail != "":
						override = formatSchemaRetryDirective(schemaRetryDetail)
					default:
						override = unlimitedToolExecutionDirective
					}
				}
				sendPrompt = override + msgFlat + antiLoopDirective + effectiveTurnCue
			} else {
				curConv := w.Client.CurrentConversationIDFor(sessionKey, sendModel)
				plan := planSessionTurnWithReason(sessionKey, w.ID, curConv, msgFlat)
				if plan.needReset {
					// Restore the recorded upstream conversation pointer after a
					// pool reload recreated this worker (see stream path).
					if conv, respID, choiceID, lastWorker, valid := peekSessionTurnConv(sessionKey); valid && conv != "" && lastWorker == w.ID && curConv == "" {
						w.Client.RestoreConversationStateSession(sessionKey, sendModel, conv, respID, choiceID)
						plan = planSessionTurnWithReason(sessionKey, w.ID, conv, msgFlat)
					}
				}
				replayReason = plan.reason
				lastDeltaLen = plan.deltaBytes
				if plan.needReset {
					RecordDeltaReplay(plan.reason)
					// Full replay: message transcript, turn cue, tool schemas.
					w.Client.ResetConversationStateSession(sessionKey, sendModel)
					sendPrompt = msgFlat + antiLoopDirective + effectiveTurnCue
				} else {
					sendPrompt = plan.prompt + antiLoopDirective + effectiveTurnCue
				}
			}
			lastSendPromptLen = len(sendPrompt)
			r, err := w.Client.AskModelSession(sendPrompt, sendModel, sessionKey)
			if err != nil {
				if shouldRetryTransport(err, transportRetries) {
					// A 1097 expiry or a silent upstream stall is a transport
					// incident, not a model failure: reset the conversation and
					// replay on a fresh one without consuming the functional
					// recovery budget.
					transportRetries++
					log.Printf("⚠️ Non-stream transport incident %d/%d (%v), retrying on a fresh upstream conversation without consuming a generation attempt", transportRetries, maxTransportRetries, err)
					w.Client.ResetConversationStateSession(sessionKey, sendModel)
					truncationRetry = false
					schemaRetryDetail = ""
					pseudoReportRetry = false
					unbackedRetry = false
					invalidateSessionTurn(sessionKey)
					forceFull = true
					// The loop's post statement re-increments attempt, so this
					// replay keeps the same functional slot.
					attempt--
					continue
				}
				return nil, err
			}
			// An aborted generation or a thought-only turn executed nothing.
			// Charge it to the dedicated technical budget instead of a
			// functional attempt meant for pseudo-reports, refusals, schema
			// errors, or truncations.
			if len(req.Tools) > 0 && isEmptyGenerationOutput(r.Text, req.Tools) && emptyGenerationRetries < maxEmptyGenerationRetries {
				recordTurnClass("incomplete", model)
				emptyGenerationRetries++
				// Escalate the depth on the retry: an identical empty turn at
				// the same level tends to fail identically.
				if escalated := gemini.EscalateThinkingModel(sendModel); escalated != sendModel {
					w.Client.ResetConversationStateSession(sessionKey, sendModel)
					log.Printf("⚠️ Empty/thought-only generation %d/%d: escalating thinking model %q -> %q", emptyGenerationRetries, maxEmptyGenerationRetries, sendModel, escalated)
					sendModel = escalated
				} else {
					log.Printf("⚠️ Empty/thought-only generation %d/%d, retrying on a fresh upstream conversation without consuming a generation attempt", emptyGenerationRetries, maxEmptyGenerationRetries)
				}
				truncationRetry = false
				schemaRetryDetail = ""
				pseudoReportRetry = false
				unbackedRetry = false
				invalidateSessionTurn(sessionKey)
				forceFull = true
				// The loop's post statement re-increments attempt, so this
				// replay keeps the same functional slot.
				attempt--
				continue
			}
			if attempt+1 < maxAttempts {
				if gemini.HasTruncatedMutationToolCall(r.Text) {
					RecordArrangerRejection("mid_value_truncation")
					RecordQuarantine("truncated")
					recordTurnClass("untagged_mutation", model)
					log.Printf("⚠️ Truncated mutation tool call detected, retrying once with an abstract truncation directive")
					truncationRetry = true
					schemaRetryDetail = ""
					pseudoReportRetry = false
					unbackedRetry = false
					invalidateSessionTurn(sessionKey)
					forceFull = true
					continue
				}
				if invalid, detail := gemini.HasInvalidSchemaToolCall(r.Text, req.Tools); invalid {
					RecordArrangerRejection(arrangerRejectionReason(detail))
					RecordQuarantine("schema_invalid")
					recordTurnClass("schema_invalid", model)
					log.Printf("⚠️ Invalid tool-call schema (%s), retrying with an abstract error directive", detail)
					schemaRetryDetail = detail
					truncationRetry = false
					pseudoReportRetry = false
					unbackedRetry = false
					invalidateSessionTurn(sessionKey)
					forceFull = true
					continue
				}
				if len(req.Tools) > 0 && gemini.ArrangeGeminiOutputWithTools(r.Text, req.Tools).UntaggedMutation {
					RecordArrangerRejection("untagged_mutation")
					RecordQuarantine("untagged_mutation")
					recordTurnClass("untagged_mutation", model)
					log.Printf("⚠️ Untagged mutating tool JSON detected in prose, retrying with truncation directive")
					truncationRetry = true
					schemaRetryDetail = ""
					pseudoReportRetry = false
					unbackedRetry = false
					invalidateSessionTurn(sessionKey)
					forceFull = true
					continue
				}
				if len(req.Tools) > 0 && isUnbackedCompletion(r.Text, req.Tools, sessionKey) {
					RecordArrangerRejection("unbacked_completion")
					recordTurnClass("unbacked_completion", model)
					log.Printf("⚠️ Unbacked completion claim detected (S7), retrying with ground-truth directive")
					pseudoReportRetry = false
					truncationRetry = false
					schemaRetryDetail = ""
					unbackedRetry = true
					invalidateSessionTurn(sessionKey)
					forceFull = true
					continue
				}
				if isUnusableModelOutput(r.Text, req.Tools) {
					class := classifyTurnOutput(r.Text, gemini.ArrangeGeminiOutputWithTools(r.Text, req.Tools), req.Tools, sessionKey)
					recordTurnClass(class, model)
					if len(req.Tools) > 0 && isPseudoProgressReport(r.Text, req.Tools) {
						log.Printf("⚠️ Pseudo-report without a tool call (%d chars), retrying with an anti-summary directive", len(r.Text))
						pseudoReportRetry = true
						truncationRetry = false
						schemaRetryDetail = ""
						unbackedRetry = false
					} else {
						if len(req.Tools) > 0 && isToolRefusalOutput(r.Text, req.Tools) {
							RecordRefusalRetry()
						}
						truncationRetry = false
						schemaRetryDetail = ""
						pseudoReportRetry = false
						unbackedRetry = false
						log.Printf("⚠️ Unusable model output or refusal (%d chars), retrying on a fresh upstream conversation", len(r.Text))
					}
					invalidateSessionTurn(sessionKey)
					forceFull = true
					continue
				}
			}
			return r, nil
		}
		return nil, fmt.Errorf("unreachable")
	})
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return c.Status(504).JSON(fiber.Map{"error": "Request timed out in worker queue."})
		}
		if strings.Contains(strings.ToLower(err.Error()), "rate_limit") || strings.Contains(err.Error(), "429") || strings.Contains(strings.ToLower(err.Error()), "cooldown") {
			return c.Status(429).JSON(fiber.Map{
				"error": fiber.Map{
					"message": err.Error(),
					"type":    "rate_limit_error",
					"code":    429,
				},
			})
		}
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	emitSummary := func(class, finish string, visibleLen int) {
		emitTurnSummary(sessionKey, model, lastSendPromptLen, lastDeltaLen, replayReason, resp.ConversationID, visibleLen, len(req.Tools), class, finish)
	}

	if len(req.Tools) > 0 {
		if invalid, detail := gemini.HasInvalidSchemaToolCall(resp.Text, req.Tools); invalid {
			RecordArrangerRejection(arrangerRejectionReason(detail))
			RecordQuarantine("schema_invalid")
			recordTurnClass("schema_invalid", model)
			log.Printf("❌ Upstream tool call failed schema validation after retry: %s", detail)
			invalidateSessionTurn(sessionKey)
			emitSummary("schema_invalid", "error", len(resp.Text))
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
				"error": fmt.Sprintf("upstream tool call failed schema validation: %s", detail),
			})
		}
	}

	// Commit the turn only if the model produced usable output; a blocked or
	// empty generation invalidates the session conversation so the next
	// request resets it and replays the full transcript.
	if isUnusableModelOutput(resp.Text, req.Tools) || isUnbackedCompletion(resp.Text, req.Tools, sessionKey) {
		invalidateSessionTurn(sessionKey)
	} else {
		lastWorkerID := ""
		if usedWorker != nil {
			lastWorkerID = usedWorker.ID
		}
		commitSessionTurn(sessionKey, msgFlat, resp.ConversationID, resp.ResponseID, resp.ChoiceID, lastWorkerID)
	}

	arranged := gemini.ArrangeGeminiOutputWithTools(resp.Text, req.Tools)
	recordArrangerRepairs(arranged)
	if arranged.IsToolCall {
		arranged.ToolCalls = gemini.ThrottleMutatingToolCalls(arranged.ToolCalls)
		if len(arranged.ToolCalls) == 0 {
			arranged.IsToolCall = false
			arranged.FinishReason = "stop"
		}
	}
	if gemini.HasTruncatedMutationToolCall(resp.Text) {
		RecordArrangerRejection("mid_value_truncation")
		RecordQuarantine("truncated")
		recordTurnClass("untagged_mutation", model)
		emitSummary("untagged_mutation", "error", len(arranged.CleanText))
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "upstream tool call was truncated mid-value; no mutation was executed",
		})
	}
	// Mirror the streaming barrier: a tool-bearing turn with neither a call
	// nor usable visible content must never be serialized as a successful
	// completion, including a pseudo-report, refusal, untagged mutation, or
	// unbacked completion claim that survived retries.
	if len(req.Tools) > 0 &&
		(shouldBlockUnusableToolTurn(arranged, resp.Text, req.Tools) ||
			isUnbackedCompletion(resp.Text, req.Tools, sessionKey)) {
		RecordArrangerRejection("unusable_action_with_tools")
		class := classifyTurnOutput(resp.Text, arranged, req.Tools, sessionKey)
		recordTurnClass(class, model)
		if class == "untagged_mutation" {
			RecordQuarantine("untagged_mutation")
		}
		invalidateSessionTurn(sessionKey)
		emitSummary(class, "error", len(arranged.CleanText))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": unusableActionErrorMessage,
		})
	}
	if arranged.IsToolCall {
		recordSessionToolCalls(sessionKey, arranged.ToolCalls)
		recordSessionLedgerToolCalls(sessionKey, arranged.ToolCalls)
	}
	pTokens := CountTokensLimited(prompt)
	cTokens := CountTokensLimited(arranged.CleanText)

	var message OpenAIChatMessage
	if arranged.IsToolCall {
		var toolCalls []OpenAIToolCall
		for _, tc := range arranged.ToolCalls {
			toolCalls = append(toolCalls, OpenAIToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: OpenAIToolCallFunction{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		message = OpenAIChatMessage{
			Role:      "assistant",
			Content:   nil,
			ToolCalls: toolCalls,
		}
		if arranged.ReasoningContent != "" {
			rc := arranged.ReasoningContent
			message.ReasoningContent = &rc
		}
	} else {
		cleanText := arranged.CleanText
		message = OpenAIChatMessage{
			Role:    "assistant",
			Content: contentRawString(cleanText),
		}
		if arranged.ReasoningContent != "" {
			rc := arranged.ReasoningContent
			message.ReasoningContent = &rc
		}
	}

	openAIResp := OpenAIChatCompletionResponse{
		ID:      "chatcmpl-" + uuid.NewString()[:12],
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []OpenAIChoice{
			{
				Index:        0,
				Message:      message,
				FinishReason: arranged.FinishReason,
			},
		},
		Usage: &OpenAIUsage{
			PromptTokens:     pTokens,
			CompletionTokens: cTokens,
			TotalTokens:      pTokens + cTokens,
		},
	}

	// Save to SQLite database asynchronously through the bounded history worker.
	userIP := c.IP()
	tier, accID := "free", ""
	if usedWorker != nil {
		tier = usedWorker.Tier
		accID = usedWorker.ID
	}
	history := GetHistoryService()
	history.LogTurn(resp.ConversationID, sessionID, prompt, arranged.CleanText, model, pTokens, cTokens, tier, accID)
	history.LogRequest("/v1/chat/completions", "POST", userIP, 200, resp.Elapsed*1000)

	class := classifyTurnOutput(resp.Text, arranged, req.Tools, sessionKey)
	recordTurnClass(class, model)
	emitSummary(class, arranged.FinishReason, len(arranged.CleanText))

	return c.JSON(openAIResp)
}
