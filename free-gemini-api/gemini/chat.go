package gemini

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

type ModelConversationState struct {
	ConversationID string `json:"conversation_id"`
	ResponseID     string `json:"response_id"`
	ChoiceID       string `json:"choice_id"`
}

// optionalConversationID marshals an absent conversation pointer as JSON null
// rather than an empty string, matching the web client's "start a new chat"
// payload.
func optionalConversationID(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// temporaryChatFlagIndex is the position in the request array that marks the
// chat as temporary. Temporary chats are not saved to the account's history and
// do not draw on personal context/memory, which is what keeps one client
// conversation from leaking into another when several harness chats share a
// Google account.
const temporaryChatFlagIndex = 45

// buildRequestStruct assembles the outer request array. Only the first three
// positions are populated (messages, language, conversation metadata) plus the
// temporary-chat flag; the remaining positions are sparse and left nil.
func buildRequestStruct(msgInner, msgLang, msgContext any, temporary bool) []interface{} {
	req := make([]interface{}, temporaryChatFlagIndex+1)
	req[0] = msgInner
	req[1] = msgLang
	req[2] = msgContext
	if temporary {
		req[temporaryChatFlagIndex] = 1
	}
	return req
}

// temporaryChatsEnabled reports whether upstream turns should be marked as
// temporary chats. Temporary chats are not saved to the account's history and
// do not draw on personal context/memory. This proxy fronts one Google account
// for many independent harness conversations, so without it a chat in one
// client can surface in another (Gemini's personal context recalls recent
// chats). Set GEMINI_TEMPORARY_CHATS=0 to opt out.
func temporaryChatsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GEMINI_TEMPORARY_CHATS"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// convStateKey builds the conversation-state map key for a (session, model)
// pair. An empty sessionKey selects the legacy shared state used by the native
// endpoints (unified /chat, media, image and video generation).
func convStateKey(sessionKey, modelID string) string {
	if sessionKey == "" {
		return modelID
	}
	return modelID + "\x00" + sessionKey
}

// getConvStateFor reads conversation state for one (session, model) pair.
// Legacy callers (empty sessionKey) fall back to the client-wide ids so
// existing media/chat flows keep their behavior; session-scoped callers get a
// fresh conversation when no state exists yet.
func (c *GeminiClient) getConvStateFor(sessionKey, modelID string) (string, string, string) {
	c.convMu.Lock()
	defer c.convMu.Unlock()
	if c.convStates == nil {
		c.convStates = make(map[string]*ModelConversationState)
	}
	if state, ok := c.convStates[convStateKey(sessionKey, modelID)]; ok && state != nil {
		return state.ConversationID, state.ResponseID, state.ChoiceID
	}
	if sessionKey == "" {
		return c.ConversationID, c.ResponseID, c.ChoiceID
	}
	return "", "", ""
}

// CurrentConversationIDFor exposes the live upstream conversation id for a
// (session, model) pair so callers can verify which conversation a session is
// bound to before appending a delta turn.
func (c *GeminiClient) CurrentConversationIDFor(sessionKey, modelID string) string {
	convID, _, _ := c.getConvStateFor(sessionKey, modelID)
	return convID
}

func (c *GeminiClient) updateConvState(modelID, convID, respID, choiceID string) {
	c.updateConvStateFor("", modelID, convID, respID, choiceID)
}

func (c *GeminiClient) updateConvStateFor(sessionKey, modelID, convID, respID, choiceID string) {
	c.convMu.Lock()
	defer c.convMu.Unlock()
	if c.convStates == nil {
		c.convStates = make(map[string]*ModelConversationState)
	}
	c.convStates[convStateKey(sessionKey, modelID)] = &ModelConversationState{
		ConversationID: convID,
		ResponseID:     respID,
		ChoiceID:       choiceID,
	}
	if sessionKey == "" {
		c.ConversationID = convID
		c.ResponseID = respID
		c.ChoiceID = choiceID
	}
}

func (c *GeminiClient) Ask(prompt string) (*GeminiResponse, error) {
	return c.AskModel(prompt, "")
}

func (c *GeminiClient) AskModel(prompt string, modelName string) (*GeminiResponse, error) {
	return c.AskWithToolModelSession(prompt, "", modelName, "")
}

// AskStream sends a prompt and streams text chunks via callback as they arrive
func (c *GeminiClient) AskStream(prompt string, onChunk func(text string)) (*GeminiResponse, error) {
	return c.AskStreamModel(prompt, "", onChunk)
}

// AskModelSession asks with a session-scoped upstream conversation: state is
// stored per (sessionKey, model) pair, so distinct client sessions never share
// one gemini.google.com conversation.
func (c *GeminiClient) AskModelSession(prompt string, modelName string, sessionKey string) (*GeminiResponse, error) {
	return c.AskWithToolModelSession(prompt, "", modelName, sessionKey)
}

// ResetConversationStateSession clears the sticky upstream conversation state
// for one (session, model) pair so the next Ask*Session call starts a fresh
// Gemini conversation.
func (c *GeminiClient) ResetConversationStateSession(sessionKey, modelID string) {
	c.convMu.Lock()
	defer c.convMu.Unlock()
	if c.convStates == nil {
		return
	}
	delete(c.convStates, convStateKey(sessionKey, modelID))
}

// RestoreConversationStateSession re-installs a known upstream conversation
// pointer for a (session, model) pair. Used after a pool reload recreates the
// worker client: the conversation itself still lives on gemini.google.com,
// only the client-side pointer was lost, so the session can keep appending
// deltas to it (same Google account).
func (c *GeminiClient) RestoreConversationStateSession(sessionKey, modelID, convID, respID, choiceID string) {
	if convID == "" {
		return
	}
	c.updateConvStateFor(sessionKey, modelID, convID, respID, choiceID)
}

// AskStreamModel streams with a specific target model
func (c *GeminiClient) AskStreamModel(prompt string, modelName string, onChunk func(text string)) (*GeminiResponse, error) {
	return c.AskStreamModelSession(prompt, modelName, onChunk, "")
}

// AskStreamModelSession streams with a session-scoped upstream conversation
// (see AskModelSession).
func (c *GeminiClient) AskStreamModelSession(prompt string, modelName string, onChunk func(text string), sessionKey string) (*GeminiResponse, error) {
	start := time.Now()

	var response *GeminiResponse
	execErr := c.executeWithRetry("AskStream", func() error {
		var err error
		response, err = c.executeStreamRequestSession(prompt, modelName, onChunk, sessionKey)
		return err
	})

	if execErr != nil {
		return nil, execErr
	}

	response.Elapsed = time.Since(start).Seconds()
	return response, nil
}

func (c *GeminiClient) executeStreamRequestSession(prompt string, modelName string, onChunk func(text string), sessionKey string) (*GeminiResponse, error) {
	if err := c.ensureInit(); err != nil {
		return nil, err
	}

	modelCfg := GetModelRegistry().Resolve(modelName)

	reqID := c.nextReqID()

	convID, respID, choiceID := c.getConvStateFor(sessionKey, modelCfg.ID)

	msgInner := []interface{}{prompt, 0, nil, []interface{}{}, nil, nil, 0}
	msgLang := []string{"en-GB"}
	msgContext := []interface{}{optionalConversationID(convID), optionalConversationID(respID), optionalConversationID(choiceID), nil, nil, nil, nil, nil, nil, ""}
	msgStruct := buildRequestStruct(msgInner, msgLang, msgContext, temporaryChatsEnabled())

	msgJSON, _ := json.Marshal(msgStruct)
	fReqVal := []interface{}{nil, string(msgJSON)}
	fReqJSON, _ := json.Marshal(fReqVal)

	snlM0e, fsid, bl := c.sessionTokens()
	data := url.Values{}
	data.Set("f.req", string(fReqJSON))
	if snlM0e != "" {
		data.Set("at", snlM0e)
	}

	urlStr := fmt.Sprintf("%s%s/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate", geminiWebBaseURL, c.getAccountPrefix())
	query := url.Values{}
	query.Add("bl", bl)
	query.Add("_reqid", reqID)
	query.Add("rt", "c")
	if fsid != "" {
		query.Add("f.sid", fsid)
	}
	fullURL := urlStr + "?" + query.Encode()

	var rawBody string
	var err error

	if c.useQUIC.Load() {
		rawBody, err = c.doStreamRequestQUIC(fullURL, data, modelCfg, onChunk)
		if err != nil {
			log.Printf("⚠️ HTTP/3 QUIC stream failed (%v). Falling back to HTTP/2 TLS...", err)
			c.useQUIC.Store(false)
			rawBody, err = c.doStreamRequestHTTP2(fullURL, data, modelCfg, onChunk)
		}
	} else {
		rawBody, err = c.doStreamRequestHTTP2(fullURL, data, modelCfg, onChunk)
	}

	if err != nil {
		if errors.Is(err, ErrUpstreamStreamStalled) {
			// The stream produced no bytes: the current upstream conversation
			// (possibly a very large replay) is the most likely culprit. Drop
			// the pointer so the API retry starts a genuinely fresh session.
			c.ResetConversationStateSession(sessionKey, modelCfg.ID)
		}
		return nil, err
	}

	if isConversationExpiredBody(rawBody) {
		log.Printf("⚠️ [chat.go] Upstream returned 1097 (Can't continue conversation); resetting session conversation")
		c.ResetConversationStateSession(sessionKey, modelCfg.ID)
		return nil, ErrConversationExpired
	}

	response := &GeminiResponse{}
	parseResponse(rawBody, response)

	if response.ConversationID != "" {
		c.updateConvStateFor(sessionKey, modelCfg.ID, response.ConversationID, response.ResponseID, response.ChoiceID)
	}

	// Video auto-detect
	if response.ConversationID != "" &&
		(strings.Contains(response.Text, "video_gen_chip") || strings.Contains(response.Text, "generating your video")) {
		c.pollVideoURL(response)
	}

	if response.ConversationID == "" && response.Text == "" {
		log.Printf("⚠️ [chat.go] Empty stream response received. rawBody len=%d snippet: %q", len(rawBody), func() string {
			if len(rawBody) > 1000 {
				return rawBody[:1000]
			}
			return rawBody
		}())
		return nil, fmt.Errorf("empty stream response received")
	}

	return response, nil
}

// isArtifactStreamText reports whether a streamed snapshot is an opaque
// googleusercontent artifact handle rather than model prose. Gemini's stream
// occasionally carries such a URL in the text slot (generated media/UI
// artifacts); emitting it as chat text corrupts the answer.
func isArtifactStreamText(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	if !strings.HasPrefix(t, "http://googleusercontent.com/") && !strings.HasPrefix(t, "https://googleusercontent.com/") {
		return false
	}
	return !strings.ContainsAny(t, " \t\r\n")
}

// nextStreamDelta projects a fresh upstream snapshot onto the text already
// emitted. Gemini's StreamGenerate emits both growing snapshots of the answer
// and, near the end, full re-renders of it (sometimes repeated); naively
// appending every snapshot duplicates whole paragraphs. Only a snapshot that
// genuinely extends what the client already has produces a delta.
func nextStreamDelta(sent, snapshot string) (delta, newSent string) {
	if snapshot == "" || snapshot == sent {
		return "", sent
	}
	if strings.HasPrefix(snapshot, sent) {
		return snapshot[len(sent):], snapshot
	}
	// Divergent snapshot: a re-render or duplicate of text already delivered,
	// not an extension. Emitting it would duplicate content.
	return "", sent
}

// readAndProcessStream consumes the upstream SSE-like body line by line.
// Transport callers wrap the body with newStallGuardReader, so a body that
// stops producing bytes does not block here forever: the guard cancels the
// request context and surfaces ErrUpstreamStreamStalled.
func (c *GeminiClient) readAndProcessStream(body io.Reader, onChunk func(text string)) (string, error) {
	var rawBody strings.Builder
	reader := bufio.NewReader(body)
	sent := ""

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			rawBody.WriteString(line)
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[[\"wrb.fr\"") {
				tempResp := &GeminiResponse{}
				parseResponse(trimmed, tempResp)

				snapshot := tempResp.Text
				if snapshot != "" && !isArtifactStreamText(snapshot) {
					delta, newSent := nextStreamDelta(sent, snapshot)
					if delta != "" && onChunk != nil {
						onChunk(delta)
					}
					sent = newSent
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return rawBody.String(), err
		}
	}

	return rawBody.String(), nil
}

func (c *GeminiClient) AskWithTool(prompt string, tool string) (*GeminiResponse, error) {
	return c.AskWithToolModel(prompt, tool, "")
}

// AskWithToolModel requests with an optional native tool (music_gen etc.) on
// the legacy shared conversation state.
func (c *GeminiClient) AskWithToolModel(prompt string, tool string, modelName string) (*GeminiResponse, error) {
	return c.AskWithToolModelSession(prompt, tool, modelName, "")
}

// AskWithToolModelSession requests with a session-scoped upstream conversation
// (see AskModelSession).
func (c *GeminiClient) AskWithToolModelSession(prompt string, tool string, modelName string, sessionKey string) (*GeminiResponse, error) {
	start := time.Now()
	if err := c.ensureInit(); err != nil {
		return nil, err
	}

	modelCfg := GetModelRegistry().Resolve(modelName)

	var response *GeminiResponse
	var err error
	execErr := c.executeWithRetry("AskWithTool", func() error {
		_, response, err = c.sendRequestSession(prompt, tool, nil, modelCfg, sessionKey)
		if err != nil {
			return err
		}
		if response.ConversationID == "" && response.Text == "" {
			return fmt.Errorf("empty response received (possible session expiry)")
		}
		return nil
	})

	if execErr != nil {
		return nil, execErr
	}

	if response.ConversationID != "" {
		c.updateConvStateFor(sessionKey, modelCfg.ID, response.ConversationID, response.ResponseID, response.ChoiceID)
	}

	// Music fallback: if URL didn't come in first response, quick poll
	if tool == "music_gen" && len(response.Music) == 0 && response.ConversationID != "" {
		log.Println("🎵 Music URL not in response, polling...")
		rawConv := strings.TrimPrefix(response.ConversationID, "c_")
		re := regexp.MustCompile(`https?://contribution\.usercontent\.google\.com/download[^"\\` + "`" + `\s]+`)

		for i := 0; i < 6; i++ {
			time.Sleep(5 * time.Second)
			body, err := c.pollConversation(rawConv)
			if err != nil {
				continue
			}
			body = strings.ReplaceAll(body, `\u0026`, "&")
			body = strings.ReplaceAll(body, `\u003d`, "=")
			if m := re.FindString(body); m != "" {
				track := MusicTrack{DownloadURL: m}
				if idx := strings.Index(m, "filename="); idx != -1 {
					fname := m[idx+9:]
					if ai := strings.Index(fname, "&"); ai != -1 {
						fname = fname[:ai]
					}
					fname = strings.ReplaceAll(fname, "_", " ")
					fname = strings.TrimSuffix(strings.TrimSuffix(fname, ".mp3"), ".mp4")
					track.Title = fname
				}
				response.Music = append(response.Music, track)
				log.Println("✅ Music URL found via poll")
				break
			}
		}
	}

	// Auto-detect video generation and poll
	if tool == "" && response.ConversationID != "" &&
		(strings.Contains(response.Text, "video_gen_chip") || strings.Contains(response.Text, "generating your video")) {
		c.pollVideoURL(response)
	}

	response.Elapsed = time.Since(start).Seconds()
	return response, nil
}

// AskVideo generates a video via chat prompt and polls hNvQHb for download URL.
// Flow: sendRequest(prompt) → poll hNvQHb(convID) for contribution.usercontent URL
func (c *GeminiClient) AskVideo(prompt string) (*GeminiResponse, error) {
	start := time.Now()
	if err := c.ensureInit(); err != nil {
		return nil, err
	}

	// Ensure prompt triggers video generation
	lower := strings.ToLower(prompt)
	if !strings.Contains(lower, "generate a video") && !strings.Contains(lower, "create a video") && !strings.Contains(lower, "make a video") {
		prompt = "Generate a video of: " + prompt
	}

	modelCfg := GetModelRegistry().Resolve("")

	var response *GeminiResponse
	var err error
	execErr := c.executeWithRetry("AskVideo", func() error {
		_, response, err = c.sendRequest(prompt, "", nil, modelCfg)
		if err != nil {
			return err
		}
		if response.ConversationID == "" && response.Text == "" {
			return fmt.Errorf("empty response received for video (possible session expiry)")
		}
		return nil
	})

	if execErr != nil {
		return nil, execErr
	}

	if response.ConversationID != "" {
		c.updateConvState(modelCfg.ID, response.ConversationID, response.ResponseID, response.ChoiceID)
	}

	log.Printf("🎬 Video response: conv=%s, text=%s", response.ConversationID, response.Text[:min(len(response.Text), 100)])

	// If no video_gen_chip in response, video wasn't triggered
	if !strings.Contains(response.Text, "video_gen_chip") && !strings.Contains(response.Text, "generating your video") {
		log.Println("⚠️ Video generation not triggered — Gemini didn't recognize video intent")
		response.Elapsed = time.Since(start).Seconds()
		return response, nil
	}

	if response.ConversationID == "" {
		response.Elapsed = time.Since(start).Seconds()
		return response, nil
	}

	c.pollVideoURL(response)

	response.Elapsed = time.Since(start).Seconds()
	return response, nil
}

// CallRPC sends a batchexecute request for any given RPC ID
func (c *GeminiClient) CallRPC(rpcID, payload string) (string, error) {
	reqID := c.nextReqID()

	wrapperJSON, _ := json.Marshal([][][]interface{}{{{rpcID, payload, nil, "generic"}}})

	snlM0e, fsid, bl := c.sessionTokens()
	body := url.Values{}
	body.Set("f.req", string(wrapperJSON))
	if snlM0e != "" {
		body.Set("at", snlM0e)
	}

	urlStr := fmt.Sprintf("%s%s/_/BardChatUi/data/batchexecute", geminiWebBaseURL, c.getAccountPrefix())

	req, err := http.NewRequest("POST", urlStr, strings.NewReader(body.Encode()))
	if err != nil {
		return "", err
	}

	q := req.URL.Query()
	q.Add("bl", bl)
	q.Add("_reqid", reqID)
	q.Add("rt", "c")
	if fsid != "" {
		q.Add("f.sid", fsid)
	}
	q.Add("rpcids", rpcID)
	q.Add("source-path", "/app")
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("sec-ch-ua", CurrentSecChUa)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", CurrentPlatform)
	req.Header.Set("User-Agent", CurrentChromeUA)
	req.Header.Set("Origin", geminiWebBaseURL)
	req.Header.Set("Referer", fmt.Sprintf("%s%s/app", geminiWebBaseURL, c.getAccountPrefix()))
	req.Header.Set("X-Same-Domain", "1")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	return string(bodyBytes), nil
}

// pollConversation calls hNvQHb RPC (music-style payload) for music polling
func (c *GeminiClient) pollConversation(convID string) (string, error) {
	payload := fmt.Sprintf(`[null,"%s"]`, convID)
	return c.CallRPC("hNvQHb", payload)
}

func (c *GeminiClient) sendRequest(prompt string, tool string, imageRef []interface{}, modelCfg *ModelConfig) (string, *GeminiResponse, error) {
	return c.sendRequestSession(prompt, tool, imageRef, modelCfg, "")
}

// sendRequestSession sends one upstream turn using the conversation state
// scoped to sessionKey (empty = legacy shared state).
func (c *GeminiClient) sendRequestSession(prompt string, tool string, imageRef []interface{}, modelCfg *ModelConfig, sessionKey string) (string, *GeminiResponse, error) {
	if modelCfg == nil {
		modelCfg = GetModelRegistry().Resolve("")
	}
	reqID := c.nextReqID()

	convID, respID, choiceID := c.getConvStateFor(sessionKey, modelCfg.ID)

	imagesPayload := imageRef
	if imagesPayload == nil {
		imagesPayload = []interface{}{}
	}
	msgInner := []interface{}{prompt, 0, nil, imagesPayload, nil, nil, 0}
	msgLang := []string{"en-GB"}
	msgContext := []interface{}{optionalConversationID(convID), optionalConversationID(respID), optionalConversationID(choiceID), nil, nil, nil, nil, nil, nil, ""}
	msgStruct := buildRequestStruct(msgInner, msgLang, msgContext, temporaryChatsEnabled())

	if tool != "" {
		for len(msgStruct) < 33 {
			msgStruct = append(msgStruct, nil)
		}
		msgStruct = append(msgStruct, []string{tool})
	}

	msgJSON, _ := json.Marshal(msgStruct)
	fReqVal := []interface{}{nil, string(msgJSON)}
	fReqJSON, _ := json.Marshal(fReqVal)

	snlM0e, fsid, bl := c.sessionTokens()
	data := url.Values{}
	data.Set("f.req", string(fReqJSON))
	if snlM0e != "" {
		data.Set("at", snlM0e)
	}

	urlStr := fmt.Sprintf("%s%s/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate", geminiWebBaseURL, c.getAccountPrefix())
	query := url.Values{}
	query.Add("bl", bl)
	query.Add("_reqid", reqID)
	query.Add("rt", "c")
	if fsid != "" {
		query.Add("f.sid", fsid)
	}
	fullURL := urlStr + "?" + query.Encode()

	var rawBody string
	var err error

	if c.useQUIC.Load() {
		rawBody, err = c.sendRequestQUIC(fullURL, data, modelCfg)
		if err != nil {
			log.Printf("⚠️ HTTP/3 QUIC request failed (%v). Falling back to HTTP/2 TLS...", err)
			c.useQUIC.Store(false)
			rawBody, err = c.sendRequestHTTP2(fullURL, data, modelCfg)
		}
	} else {
		rawBody, err = c.sendRequestHTTP2(fullURL, data, modelCfg)
	}

	if err != nil {
		if errors.Is(err, ErrUpstreamStreamStalled) {
			c.ResetConversationStateSession(sessionKey, modelCfg.ID)
		}
		return "", nil, err
	}

	if isConversationExpiredBody(rawBody) {
		log.Printf("⚠️ [chat.go] Upstream returned 1097 (Can't continue conversation); resetting session conversation")
		c.ResetConversationStateSession(sessionKey, modelCfg.ID)
		return "", nil, ErrConversationExpired
	}

	log.Printf("Raw Body Length: %d, Response: %s", len(rawBody), rawBody[:min(len(rawBody), 500)])

	response := &GeminiResponse{}
	parseResponse(rawBody, response)

	// Direct regex scan for music download URLs in raw body
	if strings.Contains(rawBody, "contribution.usercontent.google.com") {
		musicRe := regexp.MustCompile(`https://contribution\.usercontent\.google\.com/download[^"\\)\}\s]*`)
		matches := musicRe.FindAllString(rawBody, -1)
		for _, m := range matches {
			m = strings.ReplaceAll(m, `\u0026`, "&")
			m = strings.ReplaceAll(m, `\\u0026`, "&")
			found := false
			for _, existing := range response.Music {
				if existing.DownloadURL == m {
					found = true
					break
				}
			}
			if !found {
				track := MusicTrack{DownloadURL: m}
				if idx := strings.Index(m, "filename="); idx != -1 {
					fname := m[idx+9:]
					if ampIdx := strings.Index(fname, "&"); ampIdx != -1 {
						fname = fname[:ampIdx]
					}
					fname = strings.ReplaceAll(fname, "_", " ")
					fname = strings.TrimSuffix(fname, ".mp3")
					fname = strings.TrimSuffix(fname, ".mp4")
					track.Title = fname
				}
				response.Music = append(response.Music, track)
				log.Printf("🎵 Found music URL via regex: %s", m[:min(len(m), 100)])
			}
		}
	}

	return rawBody, response, nil
}

func parseResponse(raw string, res *GeminiResponse) {
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "[[") {
			continue
		}

		var wrapper [][]interface{}
		if err := json.Unmarshal([]byte(line), &wrapper); err != nil {
			continue
		}

		for _, item := range wrapper {
			if len(item) < 3 {
				continue
			}
			wrbID, ok := item[0].(string)
			if !ok || wrbID != "wrb.fr" {
				continue
			}

			payloadStr, ok := item[2].(string)
			if !ok {
				continue
			}

			var inner []interface{}
			if err := json.Unmarshal([]byte(payloadStr), &inner); err != nil {
				continue
			}

			if len(inner) > 1 {
				if ctxArr, ok := inner[1].([]interface{}); ok && len(ctxArr) >= 2 {
					if cid, ok := ctxArr[0].(string); ok {
						res.ConversationID = cid
					}
					if rid, ok := ctxArr[1].(string); ok {
						res.ResponseID = rid
					}
				}
			}

			if len(inner) > 4 {
				if contentArr, ok := inner[4].([]interface{}); ok && len(contentArr) > 0 {
					if msgItem, ok := contentArr[0].([]interface{}); ok && len(msgItem) > 1 {
						if contentList, ok := msgItem[1].([]interface{}); ok && len(contentList) > 0 {
							if text, ok := contentList[0].(string); ok {
								res.Text = text
							}
						}
						if choiceID, ok := msgItem[0].(string); ok {
							res.ChoiceID = choiceID
						}

						if len(msgItem) > 12 {
							if imgData, ok := msgItem[12].([]interface{}); ok && len(imgData) > 7 {
								if outerList, ok := imgData[7].([]interface{}); ok && len(outerList) > 0 {
									if innerList, ok := outerList[0].([]interface{}); ok {
										for _, imgObj := range innerList {
											if imgArr, ok := imgObj.([]interface{}); ok && len(imgArr) > 0 {
												if subArr, ok := imgArr[0].([]interface{}); ok && len(subArr) > 3 {
													if deepArr, ok := subArr[3].([]interface{}); ok && len(deepArr) > 3 {
														if imgURL, ok := deepArr[3].(string); ok {
															base := strings.Split(imgURL, "=")[0]
															imgURL = base + "=s0"

															// Deduplicate: only append if not already in res.Images
															isDup := false
															for _, existing := range res.Images {
																if existing == imgURL {
																	isDup = true
																	break
																}
															}
															if !isDup {
																res.Images = append(res.Images, imgURL)
															}
														}
													}
												}
											}
										}
									}
								}
							}
						}
						probeMusicURLs(msgItem, res, 0)
					}
				}
			}
		}
	}
}

// probeMusicURLs recursively probes nested data for music download URLs
func probeMusicURLs(data interface{}, res *GeminiResponse, depth int) {
	if depth > 20 || data == nil {
		return
	}
	switch v := data.(type) {
	case string:
		if len(v) > 20 && strings.HasPrefix(v, "http") &&
			strings.Contains(v, "contribution.usercontent.google.com") {
			found := false
			for _, m := range res.Music {
				if m.DownloadURL == v {
					found = true
					break
				}
			}
			if !found {
				track := MusicTrack{DownloadURL: v}
				if idx := strings.Index(v, "filename="); idx != -1 {
					fname := v[idx+9:]
					if ampIdx := strings.Index(fname, "&"); ampIdx != -1 {
						fname = fname[:ampIdx]
					}
					fname = strings.ReplaceAll(fname, "_", " ")
					fname = strings.TrimSuffix(fname, ".mp3")
					fname = strings.TrimSuffix(fname, ".mp4")
					track.Title = fname
				}
				res.Music = append(res.Music, track)
			}
		}
	case []interface{}:
		for _, item := range v {
			probeMusicURLs(item, res, depth+1)
		}
	case map[string]interface{}:
		for _, item := range v {
			probeMusicURLs(item, res, depth+1)
		}
	}
}
