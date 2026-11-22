package api

import (
	"bufio"
	"context"
	"encoding/json"
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
	reqModel := input.Model
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

	_, model, requiredTier := GetChatService().ResolveModel(c, req.Model)

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

	// Write System directives & tool schemas at the very top of the prompt
	if systemContent != "" {
		fullPromptBuilder.WriteString("[System]: " + systemContent + "\n\n")
	}
	if tiBlock != "" {
		fullPromptBuilder.WriteString(tiBlock + "\n\n")
	}

	for _, m := range nonSystemMessages {
		contentStr := FlattenContent(m.Content)
		if strings.EqualFold(m.Role, "user") {
			prompt = contentStr
			fullPromptBuilder.WriteString("User: " + contentStr + "\n")
		} else if strings.EqualFold(m.Role, "assistant") {
			if len(m.ToolCalls) > 0 {
				tcJSON, _ := json.Marshal(m.ToolCalls)
				fullPromptBuilder.WriteString("Assistant called tools: " + string(tcJSON) + "\n")
			} else if contentStr != "" {
				fullPromptBuilder.WriteString("Assistant: " + contentStr + "\n")
			}
		} else if strings.EqualFold(m.Role, "tool") {
			fullPromptBuilder.WriteString(fmt.Sprintf("[Tool Result for %s]: %s\n", m.Name, contentStr))
		} else if strings.EqualFold(m.Role, "system") {
			fullPromptBuilder.WriteString("[System Notice]: " + contentStr + "\n")
		}
	}

	if prompt == "" && len(nonSystemMessages) > 0 {
		lastM := nonSystemMessages[len(nonSystemMessages)-1]
		prompt = FlattenContent(lastM.Content)
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	turnCue := "\nAssistant: "
	if len(nonSystemMessages) > 0 {
		lastM := nonSystemMessages[len(nonSystemMessages)-1]
		if lastM.Role == "assistant" {
			turnCue = ""
		}
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
			var emittedThoughtRunes int
			var emittedContentRunes int

			streamErr := pool.ExecuteQueuedStreamWithTier(streamCtx, sessionID, requiredTier, func(wWorker *AccountWorker) error {
				usedWorker = wWorker
				// With tools requested, chunks are buffered (never streamed to
				// the client until arranged), so a blocked/empty generation can
				// be retried invisibly on a fresh upstream conversation.
				maxAttempts := 1
				if len(req.Tools) > 0 {
					maxAttempts = 2
				}
				forceFull := false
				schemaRetryDetail := ""
				for attempt := 0; attempt < maxAttempts; attempt++ {
					var sendPrompt string
					emittedThoughtRunes = 0
					emittedContentRunes = 0
					if forceFull {
						// Previous attempt hit a blocked/empty generation, a
						// refusal, or an invalid tool call: replay everything
						// fresh with a targeted override directive.
						wWorker.Client.ResetConversationStateSession(sessionKey, model)
						override := ""
						if len(req.Tools) > 0 {
							if schemaRetryDetail != "" {
								override = formatSchemaRetryDirective(schemaRetryDetail)
							} else {
								override = "[SYSTEM DIRECTIVE: Execute the next step by emitting a <tool_call> XML block. All tools listed in [AVAILABLE TOOLS] are active and executed by the host environment. Complete the current task directly with tool calls.]\n\n"
							}
						}
						sendPrompt = override + msgFlat + turnCue
					} else {
						// Continuation strategy: continue the session's live upstream
						// conversation with just the new transcript delta, or reset and
						// replay the full transcript when that is not safe.
						curConv := wWorker.Client.CurrentConversationIDFor(sessionKey, model)
						prompt, needReset := planSessionTurn(sessionKey, curConv, msgFlat)
						if needReset {
							// The worker may have been recreated by a cookie re-sync
							// (pool reload), dropping client-side conversation state
							// while the upstream conversation still exists. Restore
							// the recorded pointer when this worker owns it.
							if conv, respID, choiceID, lastWorker, valid := peekSessionTurnConv(sessionKey); valid && conv != "" && lastWorker == wWorker.ID && curConv == "" {
								wWorker.Client.RestoreConversationStateSession(sessionKey, model, conv, respID, choiceID)
								prompt, needReset = planSessionTurn(sessionKey, conv, msgFlat)
							}
						}
						if needReset {
							// Full replay: message transcript, turn cue, tool schemas.
							wWorker.Client.ResetConversationStateSession(sessionKey, model)
							sendPrompt = msgFlat + turnCue
						} else {
							sendPrompt = prompt + turnCue
						}
					}

					accumulatedOutput.Reset()
					sr, err := wWorker.Client.AskStreamModelSession(sendPrompt, model, func(chunk string) {
						accumulatedOutput.WriteString(chunk)

						rawSoFar := accumulatedOutput.String()

						// 1. Live stream thinking tokens whenever available
						allThoughts := gemini.ExtractAvailableThoughts(rawSoFar)
						if allThoughts != "" {
							thoughtRunes := []rune(allThoughts)
							if len(thoughtRunes) > emittedThoughtRunes {
								deltaThought := string(thoughtRunes[emittedThoughtRunes:])
								emittedThoughtRunes = len(thoughtRunes)

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
						}

						if len(req.Tools) > 0 {
							// Buffer chunks when tools are requested to evaluate tool call
							return
						}

						// 2. For non-tool requests, stream content (outside thoughts)
						allContent := gemini.ExtractAvailableContent(rawSoFar)
						if allContent != "" {
							contentRunes := []rune(allContent)
							if len(contentRunes) > emittedContentRunes {
								deltaContent := string(contentRunes[emittedContentRunes:])
								emittedContentRunes = len(contentRunes)

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
						}
					}, sessionKey)
					streamResp = sr
					if err != nil {
						if attempt+1 < maxAttempts {
							log.Printf("⚠️ Stream attempt %d failed (%v), retrying on a fresh upstream conversation", attempt+1, err)
							invalidateSessionTurn(sessionKey)
							forceFull = true
							continue
						}
						return err
					}
					// Retry once on a blocked/empty generation: the poisoned
					// upstream conversation is reset and the full transcript
					// (with tool schemas) is replayed fresh.
					finalAttemptText := accumulatedOutput.String()
					if streamResp != nil && len(streamResp.Text) > len(finalAttemptText) {
						finalAttemptText = streamResp.Text
					}
					if attempt+1 < maxAttempts {
						if invalid, detail := gemini.HasInvalidSchemaToolCall(finalAttemptText, req.Tools); invalid {
							log.Printf("⚠️ Invalid tool-call schema (%s), retrying with an abstract error directive", detail)
							schemaRetryDetail = detail
							invalidateSessionTurn(sessionKey)
							forceFull = true
							continue
						}
						if isUnusableModelOutput(finalAttemptText, len(req.Tools) > 0) {
							log.Printf("⚠️ Unusable model output (%d chars), retrying on a fresh upstream conversation", len(finalAttemptText))
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
				errChunk := OpenAIChatCompletionChunk{
					ID:      chunkID,
					Object:  "chat.completion.chunk",
					Created: createdTime,
					Model:   model,
					Choices: []OpenAIStreamChoice{
						{
							Index: 0,
							Delta: OpenAIDelta{
								Role:    "assistant",
								Content: fmt.Sprintf("\n\n[Error: %v]", streamErr),
							},
							FinishReason: "stop",
						},
					},
				}
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
					log.Printf("❌ Upstream tool call failed schema validation after retry: %s", detail)
					invalidateSessionTurn(sessionKey)
					errChunk := OpenAIChatCompletionChunk{
						ID:      chunkID,
						Object:  "chat.completion.chunk",
						Created: createdTime,
						Model:   model,
						Choices: []OpenAIStreamChoice{
							{
								Index: 0,
								Delta: OpenAIDelta{
									Role:    "assistant",
									Content: fmt.Sprintf("\n\n[Error: upstream tool call failed schema validation: %s]", detail),
								},
								FinishReason: "stop",
							},
						},
					}
					if errBytes, mErr := json.Marshal(errChunk); mErr == nil {
						writeChunk(errBytes)
					}
					writeRaw("data: [DONE]\n\n")
					return
				}
			}
			if isUnusableModelOutput(finalText, len(req.Tools) > 0) {
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
			if len(req.Tools) > 0 {
				rawOutput := accumulatedOutput.String()
				if streamResp != nil && streamResp.Text != "" && len(rawOutput) < len(streamResp.Text) {
					rawOutput = streamResp.Text
				}
				arranged := gemini.ArrangeGeminiOutputWithTools(rawOutput, req.Tools)
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
					errChunk := OpenAIChatCompletionChunk{
						ID:      chunkID,
						Object:  "chat.completion.chunk",
						Created: createdTime,
						Model:   model,
						Choices: []OpenAIStreamChoice{
							{
								Index: 0,
								Delta: OpenAIDelta{
									Role:    "assistant",
									Content: "\n\n[Error: upstream tool call was truncated mid-value; no mutation was executed. Please retry.]",
								},
								FinishReason: "stop",
							},
						},
					}
					if errBytes, mErr := json.Marshal(errChunk); mErr == nil {
						writeChunk(errBytes)
					}
					writeRaw("data: [DONE]\n\n")
					return
				}
				finishReason = arranged.FinishReason
				finalContentForLog = arranged.CleanText

				// Emit any remaining reasoning tokens not yet streamed
				if arranged.ReasoningContent != "" {
					rcRunes := []rune(arranged.ReasoningContent)
					if len(rcRunes) > emittedThoughtRunes {
						deltaThought := string(rcRunes[emittedThoughtRunes:])
						emittedThoughtRunes = len(rcRunes)
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
			history := GetHistoryService()
			history.LogTurn(convID, sessionID, prompt, finalContentForLog, model, pTokens, rTokens, tier, accID)
			history.LogRequest("/v1/chat/completions", "POST", userIP, 200, float64(time.Now().Unix()-createdTime)*1000)
		})
	}

	var usedWorker *AccountWorker
	resp, err := pool.ExecuteQueuedWithTier(ctx, sessionID, requiredTier, func(w *AccountWorker) (*gemini.GeminiResponse, error) {
		usedWorker = w
		// Nothing has been emitted yet, so a blocked/empty generation can be
		// retried invisibly on a fresh upstream conversation.
		forceFull := false
		schemaRetryDetail := ""
		for attempt := 0; attempt < 2; attempt++ {
			var sendPrompt string
			if forceFull {
				// Previous attempt hit a blocked/empty generation, a refusal,
				// or an invalid tool call: replay everything fresh with a
				// targeted override directive.
				w.Client.ResetConversationStateSession(sessionKey, model)
				override := ""
				if len(req.Tools) > 0 {
					if schemaRetryDetail != "" {
						override = formatSchemaRetryDirective(schemaRetryDetail)
					} else {
						override = "[SYSTEM DIRECTIVE: Execute the next step by emitting a <tool_call> XML block. All tools listed in [AVAILABLE TOOLS] are active and executed by the host environment. Complete the current task directly with tool calls.]\n\n"
					}
				}
				sendPrompt = override + msgFlat + turnCue
			} else {
				curConv := w.Client.CurrentConversationIDFor(sessionKey, model)
				prompt, needReset := planSessionTurn(sessionKey, curConv, msgFlat)
				if needReset {
					// Restore the recorded upstream conversation pointer after a
					// pool reload recreated this worker (see stream path).
					if conv, respID, choiceID, lastWorker, valid := peekSessionTurnConv(sessionKey); valid && conv != "" && lastWorker == w.ID && curConv == "" {
						w.Client.RestoreConversationStateSession(sessionKey, model, conv, respID, choiceID)
						prompt, needReset = planSessionTurn(sessionKey, conv, msgFlat)
					}
				}
				if needReset {
					// Full replay: message transcript, turn cue, tool schemas.
					w.Client.ResetConversationStateSession(sessionKey, model)
					sendPrompt = msgFlat + turnCue
				} else {
					sendPrompt = prompt + turnCue
				}
			}
			r, err := w.Client.AskModelSession(sendPrompt, model, sessionKey)
			if err != nil {
				return nil, err
			}
			if attempt+1 < 2 {
				if invalid, detail := gemini.HasInvalidSchemaToolCall(r.Text, req.Tools); invalid {
					log.Printf("⚠️ Invalid tool-call schema (%s), retrying with an abstract error directive", detail)
					schemaRetryDetail = detail
					invalidateSessionTurn(sessionKey)
					forceFull = true
					continue
				}
				if isUnusableModelOutput(r.Text, len(req.Tools) > 0) {
					log.Printf("⚠️ Unusable model output or refusal (%d chars), retrying on a fresh upstream conversation", len(r.Text))
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

	if len(req.Tools) > 0 {
		if invalid, detail := gemini.HasInvalidSchemaToolCall(resp.Text, req.Tools); invalid {
			log.Printf("❌ Upstream tool call failed schema validation after retry: %s", detail)
			invalidateSessionTurn(sessionKey)
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
				"error": fmt.Sprintf("upstream tool call failed schema validation: %s", detail),
			})
		}
	}

	// Commit the turn only if the model produced usable output; a blocked or
	// empty generation invalidates the session conversation so the next
	// request resets it and replays the full transcript.
	if isUnusableModelOutput(resp.Text, len(req.Tools) > 0) {
		invalidateSessionTurn(sessionKey)
	} else {
		lastWorkerID := ""
		if usedWorker != nil {
			lastWorkerID = usedWorker.ID
		}
		commitSessionTurn(sessionKey, msgFlat, resp.ConversationID, resp.ResponseID, resp.ChoiceID, lastWorkerID)
	}

	arranged := gemini.ArrangeGeminiOutputWithTools(resp.Text, req.Tools)
	if arranged.IsToolCall {
		arranged.ToolCalls = gemini.ThrottleMutatingToolCalls(arranged.ToolCalls)
		if len(arranged.ToolCalls) == 0 {
			arranged.IsToolCall = false
			arranged.FinishReason = "stop"
		}
	}
	if gemini.HasTruncatedMutationToolCall(resp.Text) {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "upstream tool call was truncated mid-value; no mutation was executed",
		})
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

	return c.JSON(openAIResp)
}
