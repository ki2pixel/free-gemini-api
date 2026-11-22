# Changelog

All notable changes to the **Free Gemini API** project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [3.4.2] - 2026-11-22

### Added
- **Unified `AGENTS.md` Instruction File**:
  - Added canonical, high-signal developer guidance and agent instructions at the repository root (`AGENTS.md`), consolidating architecture boundaries, developer commands, test suites, toolchain caveats, and security gotchas from iterative validation across Gemini 4 models (`neon`, `hydrogen`, `hydrogen-task`).
- **Tool Parsing & Refusal Detection Test Suite**:
  - Added `TestIsToolRefusalOutput` in `api/session_scope_test.go` covering upstream quota/search limit refusals and manual save suggestions.
  - Added `TestArrangeGeminiOutput_UnescapedNewlinesAndQuotesInWriteTool` in `gemini/arranger_test.go` validating unescaped raw newlines and nested bash quotes in large file write tool calls.
  - Added `TestArrangeGeminiOutput_ToolsSchemaLeakPreambleCleaned` in `gemini/arranger_test.go` verifying automatic sanitization of regurgitated tool schemas.

### Fixed
- **Unescaped Control Characters & Bash Quotes in JSON Tool Arguments**:
  - Implemented `normalizeJSONStringLiterals` in `gemini/arranger.go` to automatically repair unescaped raw control characters (`\n`, `\r`, `\t`) and unescaped internal double quotes inside string literals, preventing strict Go `json.Unmarshal` failures (RFC 8259) on multiline code/file writes that previously degraded tool calls into raw chat text.
  - Added heuristic fallback extraction (`extractHeuristicToolCall`) as a resilient safety net for severely corrupted or truncated tool call payloads.
- **Prompt Leakage of Tool Schemas (`cleanText` Sanitization)**:
  - Added regex-based content sanitizers (`sanitizeGeminiContent`) in `gemini/arranger.go` stripping raw echoed `[AVAILABLE TOOLS]` schemas, `<tools>` blocks, `<output>` wrappers, and orphan subagent tags before streaming text content to clients.
  - Added negative prompting rules in `FormatToolsInstruction` instructing models never to repeat or quote the available tools schema.
- **Upstream "0 Tool Calls Remaining" False Refusal Immunization & Retry**:
  - Fixed an issue where Google's Gemini Web backend injected internal web-search limits (*"You have exceeded the maximum number of tool and search calls (0 remaining)"*), causing frontier reasoning models (`gemini-4-neon`) to falsely believe developer tools were exhausted and propose markdown text instead of invoking tools.
  - Added explicit immunization directives in `FormatToolsInstruction` clarifying that host developer tools are unlimited, external, and always active.
  - Added detection of tool refusal patterns in `isToolRefusalOutput` (`api/session_transcripts.go`) and automatic retry with strict system directives on a fresh upstream conversation (`api/chat_handler.go`).

---

## [3.4.1] - 2026-11-22

### Added
- **OpenCode CLI / IDE Integration**:
  - Added official integration documentation and provider configuration guidelines for OpenCode (`~/.config/opencode/opencode.json`).
  - Added unit test `TestArrangeGeminiOutput_MalformedClosingToolCall` in `arranger_test.go` covering malformed markdown fences on tool calls.
- **Resilient Tool Call Parsing (`extractTaggedToolCalls`)**:
  - Implemented balanced JSON extraction for `<tool_call>` blocks in `gemini/arranger.go`.
  - Added support for malformed or unclosed tool call blocks (such as markdown code fences ` ``` ` or omitted closing tags) that frequently occur during large multimodal writes.
  - Automatically strips all matched `<tool_call>` spans from `CleanText` to eliminate raw JSON leaks into chat interfaces.

### Fixed
- **Google WIZ Session Token Extraction (`thykhd` / `AFWLb...`)**:
  - Updated regular expressions in `gemini/session.go` to support Google's new `thykhd` token identifier and fallback pattern `AFWLb...` alongside legacy `SNlM0e`.
  - Added automatic removal of corrupted on-disk session cache files (`cookies/session_*.json`) inside `reinitialize()`, preventing auto-heal loops from reloading stale tokens.
  - Added validation in `gemini/monitor.go` to ignore incomplete cookie payloads missing essential session cookies (`__Secure-1PSID`, `SID`, `HSID`).
- **SSE Stream Termination & Reconnection Loops**:
  - Fixed an issue where stream errors emitted `: connected` without a valid `finish_reason` or `[DONE]` event, causing clients (such as OpenCode) to enter an 11-retry loop.
  - Streaming errors now cleanly emit an error delta chunk with `finish_reason: "stop"` followed immediately by `data: [DONE]\n\n`.
- **Multi-Turn Session Continuity Across Dynamic System Notices**:
  - Fixed session key mutation: scoped `sessionKey` strictly to initial system directives (`req.Messages[0].Role == "system"`).
  - Fixed transcript prefix breakage: dynamic system notices injected mid-stream by agent harnesses (e.g. tool catalog changes) are now preserved chronologically as inline `[System Notice]` blocks instead of being hoisted to the top of the conversation.
  - Prevents spurious session resets (`needReset = true`) and avoids replaying 50+ KB of previous terminal logs into fresh upstream chats.
- **Upstream Stream Error Retry Fallback**:
  - Added automatic retry on a fresh upstream conversation when Google's web chat returns `Can't continue conversation. Start a new one or refresh the page. (1097)` or empty stream responses.

---

## [3.4.0] - 2026-11-21

### Added
- Gemini 4 Hydrogen (`gemini-4-hydrogen`) and Gemini 4 Neon (`gemini-4-neon`) model support.
- Thinking Depth Controls (`neon-high`, `neon-medium`, `neon-low`) mapping to thinking levels 2, 4, 1.
- Agentic Task Mode (`[40]`) alongside standard Chat Mode (`[0]`).
- Dual Intelligence Analytics (SQLite WAL accounting + Excel `.xlsx` / JSON export).
- Companion Chrome Extension WebSocket cookie bridge (`ws://127.0.0.1:9226`).
- Pure Go HTTP/3 QUIC transport with Chrome 152 JA4 TLS fingerprint simulation.
