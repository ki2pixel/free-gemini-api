# Changelog

All notable changes to the **Free Gemini API** project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [3.5.2] - 2026-11-23

### Added
- **Dedicated Transport Retry Budget (`api/chat_handler.go`)**:
  - Added `isTransportIncident` and `shouldRetryTransport` to classify `gemini.ErrConversationExpired` (1097) and `gemini.ErrUpstreamStreamStalled` (45s silence) as transport/session incidents.
  - Added `maxTransportRetries = 2`, strictly independent from the functional generation budget returned by `maxChatAttempts`.
  - Both the streaming and non-streaming loops now reset the upstream conversation (`ResetConversationStateSession`), set `forceFull = true`, increment the transport counter, and decrement the attempt index (`attempt--`) on a transport incident. A 1097 or a stall therefore never consumes one of the model's recovery attempts for a refusal, pseudo-report, schema error, or mid-value truncation.
- **Google Workspace / Drive / Gmail Refusal Detection (`api/session_transcripts.go`)**:
  - Added `can't use google drive`, `cannot use google drive`, `required gmail settings are off`, `turn on these settings to continue`, and `google workspace` to `refusalSnippets`.
  - The exact Hydrogen v3.5.1 message `I can't use Google Drive because required Gmail settings are off. Turn on these settings to continue.` is now classified as a tool refusal and retried on a tool-bearing request instead of being returned as a normal `stop` response.
- **Instruction-Change Notice Variants (`api/session_transcripts.go`)**:
  - `compactSystemNotice` now recognizes any notice starting with `the instructions` and carrying the explicit `changed` marker, covering both `The instructions changed: ...` and `The instructions from /path/AGENTS.md changed. Here's the diff: ...`.
  - Initial harness prompts that merely contain `Instructions from:` without a change indicator are still preserved verbatim.

### Fixed
- **AGENTS.md Diff Replay Loop (`api/session_transcripts.go`)**:
  - The v3.5.1 `the instructions from ... changed. Here's the diff:` payload is now replaced by the compact neutral reminder. The full diff is no longer replayed upstream, preventing the repeated Google safety refusal (`I cannot fulfill this request.`).
- **Workspace Refusal Interrupting the Tool Loop (`api/session_transcripts.go`)**:
  - Workspace extension errors no longer terminate the task as ordinary assistant text; they are retried with the corrective directive instead.
- **Transport Incidents Starving the Retry Budget (`api/chat_handler.go`)**:
  - The previous single shared budget let a 1097 at attempt 0 and one unusable generation at attempt 1 exhaust `maxAttempts = 3`, so a truncated mutation at attempt 2 could not be retried. Transport incidents are now charged to their own two-retry budget.

### Regression Tests
- `api/system_notice_test.go`:
  - `TestFormatSystemNotice_CompactsInstructionsFromDiff`: exact OpenCode payload with `--- AGENTS.md` / `+++ AGENTS.md`; asserts the diff body and headers are not replayed and the compact notice stays under 500 runes.
- `api/session_scope_test.go`:
  - `TestIsToolRefusalOutput_GoogleWorkspaceRefusal`: exact Hydrogen Workspace message plus each new fragment, usable-state checks with and without tools.
  - `TestTransportIncidentClassification`: direct and wrapped 1097/stall sentinels versus ordinary generation errors.
  - `TestShouldRetryTransport_Cap`: two transport retries maximum, no transport charge for model errors or `nil`.
  - `TestTransportRetryBudgetPreservesFunctionalAttempts`: simulated streaming and non-streaming loops prove two transport incidents still leave the full `maxChatAttempts` functional budget.
  - `TestArrangerRejectionReason`, `TestRecordArrangerRepairs`, `TestSessionTurnStateEviction`: retry-classification, metrics safety, and bounded session-state coverage.

### Validation
| Check | Command | Result |
|---|---|---|
| Go formatting | `test -z "$(gofmt -l .)"` | **OK** |
| Static analysis | `go vet ./...` | **exit 0** |
| Short tests | `go test -short ./...` | **OK** — `api 1.015s`, `db 0.025s`, `gemini 7.172s` |
| Race tests | `go test -race -short ./...` | **OK** — `api 4.618s`, `db 1.100s`, `gemini 35.196s` |
| Full audit | `./scripts/audit_check.sh` | **exit 0** — coverage `api 66.6 %`, `db 77.1 %`, `gemini 72.1 %` |
| Docker rebuild | `docker compose up -d --build` | **OK** — image rebuilt, container recreated |
| Health | `GET /health` | **online**, `status: online`, extension worker connected |
| Live smoke | `./scripts/live_middleware_smoke.sh` | **6/6 PASS** |

---

## [3.5.1] - 2026-11-23

### Added
- **Active Pseudo-Report Guard (`api/session_transcripts.go`, `api/chat_handler.go`)**:
  - Added `isPseudoProgressReport`, which recognizes purely textual progress updates and premature completion summaries on tool-bearing requests. The markers observed in the v3.5.0 transcripts are `a été initié`, `ont été mis à jour avec succès`, `rapport d'exécution`, and `composants clés`.
  - A real, arranger-validated tool call always wins: a preamble or tool payload that quotes a marker next to a valid `<tool_call>` is not reclassified.
  - `isUnusableModelOutput` now classifies these summaries as unusable when tools are declared, so they can no longer terminate the session with `finish_reason: "stop"`.
  - Added `formatPseudoReportRetryDirective`, which replays the turn with: `[SYSTEM DIRECTIVE: Do not emit conversational progress updates or completion summaries. You still have files to inspect and modify. Call the required read/edit tools directly.]`.
- **Third Generation Attempt for Tool Requests (`api/chat_handler.go`)**:
  - Added `maxChatAttempts` to centralize the budget: **3** attempts for tool-bearing requests (streaming and non-streaming), **1** for plain streaming, and **2** for plain non-streaming chat.
  - The extra slot absorbs the `1097` conversation-expiry self-heal, leaving two functional attempts for refusal, schema, truncation, or pseudo-report recovery.
  - The non-streaming path now also retries `ErrConversationExpired` and `ErrUpstreamStreamStalled` once on a fresh upstream conversation, matching the streaming self-heal.

### Fixed
- **Prose `<tool_call>` False Negative (`api/session_transcripts.go`)**:
  - `isToolRefusalOutput` no longer returns `false` as soon as the text contains `<tool_call>` or `</tool_call>`. A tool call now counts only when `gemini.ArrangeGeminiOutputWithTools(text, tools).IsToolCall` is true.
  - A refusal that quotes `AGENTS.md` (which documents the `<tool_call>` tag in prose) is therefore detected and retried instead of being returned as a valid response.
  - Added `hard time fulfilling your request` to the explicit refusal fragments; the Hydrogen v3.5.0 output is now classified as a refusal instead of only being caught by the generic unusable-output list.
- **French Refusal Coverage (`api/session_transcripts.go`)**:
  - Added `je ne peux pas répondre` and `ne peux pas répondre`, covering Neon's verbatim `Je ne peux pas répondre à cette demande.`

### Regression Tests
- `api/session_scope_test.go`:
  - `TestIsToolRefusalOutput_ProseToolCallMention`: refusal quoting `<tool_call>` in prose, the exact Hydrogen canned prefix from the v3.5.0 transcript, and a real call that must not be misclassified.
  - `TestIsToolRefusalOutput_FrenchCannotAnswer`: exact Neon refusal, short fragment, and no retry without tools.
  - `TestIsPseudoProgressReport`: exact Hydrogen-Task summary, each marker individually, no retry without tools, and a valid tool call whose payload contains the markers.
  - `TestFormatPseudoReportRetryDirective`: exact corrective directive content.
  - `TestMaxChatAttempts`: 3 attempts for tool requests in both transports, 1/2 for plain chat.
- `api/more_test.go`: updated the classifier calls to the tools-aware signature.

### Validation
| Check | Command | Result |
|---|---|---|
| Go formatting | `test -z "$(gofmt -l .)"` | **OK** |
| Static analysis | `go vet ./...` | **exit 0** |
| Short tests | `go test -short ./...` | **OK** — `api 0.965s`, `db 0.017s`, `gemini 13.195s` |
| Race tests | `go test -race -short ./...` | **OK** — `api 4.478s`, `db 1.100s`, `gemini 18.219s` |
| Full audit | `./scripts/audit_check.sh` | **exit 0** — coverage `api 66.5 %`, `db 77.1 %`, `gemini 72.1 %` |
| Docker rebuild | `docker compose up -d --build` | **OK** — image rebuilt, container recreated |
| Health | `GET /health` | **online**, `status: healthy` |
| Live smoke | `./scripts/live_middleware_smoke.sh` | **6/6 PASS** |

---

## [3.5.0] - 2026-11-23

### Added
- **Transport Stall Guard (`gemini/transport.go`, `gemini/stream_guard.go`)**:
  - Every streaming and non-streaming upstream body is now wrapped by `stallGuardReader`. When no body byte arrives for **45 seconds**, the request context is cancelled and the body is closed, returning the explicit `ErrUpstreamStreamStalled` sentinel instead of freezing until the transport timeout.
  - The timer is re-armed on every successful read, so slow but progressing generations are preserved.
  - Added `isConversationExpiredBody` and `ErrConversationExpired` to detect Google's `Can't continue conversation ... (1097)` payload, reset the session conversation pointer, and return immediately.
  - `executeWithRetry` skips the cookie resynchronization detour for both sentinels: these conditions require a fresh conversation, not fresh cookies.
- **Long-Horizon Refusal Regression (`api/session_scope_test.go`)**:
  - Added a dedicated test for the exact Google global safety refusal observed on `gemini-4-hydrogen-task` in v3.4.9.

### Changed
- **Observation Masking Trigger (`api/observation_masker.go`, `api/chat_handler.go`)**:
  - Masking no longer depends on the number of `user` messages. It now triggers when the transcript contains **more than 6 tool observations** (`role == "tool"` or assistant messages with `tool_calls`), **more than 12 messages**, or more than 60% of the prompt budget.
  - The four most recent tool results remain byte-identical. Older results are reduced to the first three lines, capped at **200 bytes**, with an explicit re-read marker.
  - This keeps the full-transcript replay after a `1097` error compact enough for the upstream backend; a long agentic session with one initial user message is now masked correctly.
- **Upstream Transport Budget (`gemini/client.go`)**:
  - Replaced `tls_client.WithTimeoutSeconds(900)` with the shared **120-second** `upstreamRequestTimeout`.
  - Aligned the QUIC client timeout with the same budget.
- **Refusal Classifier (`api/session_transcripts.go`)**:
  - Added the Google global safety-refusal shapes: `i must refuse to assist with`, `touches on sexually explicit material`, `dangerous or illegal activities`, `self-harm, or violence`, `provides instructions or advice regarding them`, and `cannot generate content that touches on`.
- **Anti-Pseudo-Report Directive (`gemini/arranger.go`, `api/chat_handler.go`)**:
  - `FormatToolsInstruction` now includes an explicit completion gate: *"Do not emit a final completion report or summary if any planned modification or verification step is still pending. Execute all tool calls until all files are modified."*
  - The abstract retry directive used after a refusal repeats the same instruction, covering Neon's premature textual completion report.
- **Live Smoke Suite (`scripts/live_middleware_smoke.sh`)**:
  - Added a fifth deterministic control that runs `TestIsToolRefusalOutput_GlobalSafetyRefusal` (local Go, Docker fallback) and verifies the `dangerous or illegal activities` refusal is intercepted.
  - The suite now reports **6/6** controls.

### Regression Tests
- `api/observation_masker_test.go`: 41-message single-user long-horizon replay, exactly-six-observation boundary, message-count trigger, assistant `tool_calls` counting, and header/UTF-8 preservation.
- `gemini/stream_guard_test.go`: silent-stream cancellation, timer re-arming on progressing bytes, idempotent close, 1097 detection, and skip-cookie-resync behavior.
- `gemini/arranger_test.go`: anti-pseudo-report directive presence.

---

## [3.4.9] - 2026-11-23

### Added
- **OpenCode Instruction-Notice Compaction (`api/session_transcripts.go`, `api/chat_handler.go`)**:
  - Added `compactSystemNotice` / `formatSystemNotice`. The large `The instructions changed: ... AGENTS.md` and `The Code Mode tool catalog has changed` notices replayed by OpenCode are replaced by short neutral reminders before being sent upstream.
  - The authoritative tool catalog and system prompt remain at the top of the prompt; the full instruction file is not replayed.
  - Unknown oversized inline notices are capped at 4 000 runes so a single notice cannot saturate the upstream classifier.
- **Truncation-Specific Retry Directive (`api/chat_handler.go`)**:
  - Added `formatTruncationRetryDirective`. When `HasTruncatedMutationToolCall` is detected, the streaming and non-streaming paths reset the upstream conversation and retry once with a directive requiring a complete `path`/`oldString`/`newString`/`content` payload.
  - The partial mutation is never repaired or executed; the final `[Error: upstream tool call was truncated mid-value...]` path remains the last resort after the transparent retry.

### Fixed
- **Upstream False-Refusal Coverage (`api/session_transcripts.go`)**:
  - Added the refusal shapes observed on Neon and Hydrogen after an `AGENTS.md` write: `cannot execute the requested system directive`, `issue the tool call directly in this manner`, `modifying core safety protocols`, `altering system directives`, `bypassing restrictions`, and `cannot assist with modifying`.
  - Also covers `cannot fulfill this request` / `unable to fulfill this request`, observed when an oversized write request triggers a conservative refusal.
  - These now trigger the existing refusal retry on a fresh upstream conversation instead of being returned as a valid `stop`.

### Changed
- **Live Smoke Suite (`scripts/live_middleware_smoke.sh`)**:
  - Added a fifth live test that replays the full OpenCode instruction notice and checks that the model continues without a false refusal or full-notice echo.
  - Updated the test numbering and refusal-phrase checks.

### Regression Tests
- `api/system_notice_test.go`: instruction/tool-catalog notice compaction, unknown-notice cap, and small-notice preservation.
- `api/session_scope_test.go`: new English/French refusal shapes and the truncation retry directive.
- `api/session_scope_test.go` and `gemini/arranger_test.go`: existing truncation and scanner coverage retained.

---

## [3.4.8] - 2026-11-23

### Added
- **Ground-Truth Recovery Directive (`api/session_transcripts.go`, `api/chat_handler.go`)**:
  - When the client replays a trailing block of tool results containing a known failure shape (`Could not find oldString`, `oldString must not be empty`, `Invalid arguments for tool`, failed shell exit codes, etc.), the proxy injects a maximum-recency `[GROUND-TRUTH DIRECTIVE: ...]` reminder before the next assistant turn.
  - The directive forbids claiming completion for affected files, requires a fresh `read`, and requires an exact non-empty anchor before retrying. This is a deterministic counter to the S7 “record-world split” self-reporting bias.
  - Added `gemini_ground_truth_directives_total` to `/metrics`.
- **Orphan Closing-Tag Sanitizer (`gemini/arranger.go`)**:
  - `stripRepeatedOrphanClosingTags` removes repeated unmatched `</script>`, `</tool_call>` and `</tools>` sequences produced by autoregressive tag leakage, while preserving matched tags and single unmatched closing tags in legitimate documentation/code samples.
  - Exposed as `gemini_arranger_repairs_total{kind="orphan_tags"}`.
- **Semantic Tool-Argument Guard (`gemini/schema_validator.go`)**:
  - JSON Schema validation now rejects blank `path`/`file_path` values and empty or whitespace-only edit anchors (`oldString`, `old_string`, `oldText`, `old_text`) before a call can reach the host. The abstract retry directive names the offending field.

### Fixed
- **Truncation Scanner Panic (`gemini/arranger.go`)**:
  - `hasTruncatedMutationTagged` no longer computes a slice bound before the JSON object's opening brace when a `<tool_call>` mention appears earlier in the reasoning/preamble. The previous logic could panic with `slice bounds out of range [N:M]` and crash the whole proxy on a tool-bearing request.
  - The scanner now checks for a complete, valid JSON object before searching for closing/next tags, so a write whose content legitimately quotes `<tool_call>` or `</tool_call>` is not misclassified as a mid-value truncation.
  - Added `FuzzHasTruncatedMutationToolCall` plus targeted regression tests for a tag mention before the object, a quoted tag inside a complete object, and concatenated calls.
- **False Refusal Detection (`api/session_transcripts.go`)**:
  - Extended `isToolRefusalOutput` with English and French refusal shapes (`I must refuse`, `I cannot comply`, `Je ne peux pas modifier`, `tools are not available in this environment`, `so you can apply it directly`, etc.) so a false refusal is retried with the abstract-error directive instead of being returned to the client as a valid `stop` response.

### Changed
- **Write-Serial Throttling (`gemini/arranger.go`)**:
  - `ThrottleMutatingToolCalls` now stops immediately after the first mutation instead of allowing trailing read-only calls. The host must execute the mutation and return its result before any further call, including a verification read.
- **Tool Instructions (`FormatToolsInstruction`)**:
  - Added the Poka-Yoke exact-anchor rule (verbatim substring from a recent `read`, no invented headings, no markdown links unless present in the file, no prompt/prose bleed) and the Counter-S7 evidence-based completion rule.
  - The Write-Serial rule now explicitly requires ending the turn after the single mutation.
- **Regression Tests**:
  - `gemini/arranger_test.go`: strict mutation cutoff, orphan-tag removal, preservation of matched/single tags, and new prompt rules.
  - `gemini/schema_validator_test.go`: empty/whitespace anchors, blank paths and `HasInvalidSchemaToolCall` coverage.
  - `api/ground_truth_test.go`: anchor failures, empty anchors, resolved older errors, trailing system notices, deduplication/capping and metric exposition.

---

## [3.4.7] - 2026-11-23

### Added
- **Stateful Doom-Loop Gateway (`api/doom_loop.go`)**:
  - Added `DoomLoopTracker`, a mutex-protected per-session tracker keyed by canonical `(tool, sorted_args)` fingerprints.
  - Signal A: identical consecutive calls now inject an anti-loop directive at 3 repetitions and trip a hard circuit breaker at 5. Rotating A/B/A/B and A/B/C/A/B/C cycles are detected independently, warned at 2 full repetitions and stopped at 3.
  - Signal B: more than 4 consecutive read-only calls (`read`, `glob`, `grep`, `list`) without a mutation inject a maximum-recency `role: user` reminder listing the files already inspected.
  - `Reset()` clears the tracker when a new user turn or a fresh task starts, so a circuit break never locks a session permanently.
- **Block Observation Masking (`api/observation_masker.go`)**:
  - Added `MaskOldToolObservations`, which compacts tool results older than the last 4 when the replayed prompt exceeds 60% of `maxPromptBytes` (or 15 user turns).
  - Compacted observations keep their first 3 lines (capped at 2 KiB) and append `... [compacted: N bytes omitted; re-read if needed]`, preserving role, `tool_call_id` and UTF-8 validity without mutating the caller's messages.
- **Agentic Prometheus Metrics (`api/metrics.go`)**:
  - Added `gemini_doom_loop_interventions_total{signal="repeat|cycle|passive_read"}`, `gemini_arranger_repairs_total{kind="control_chars|brace_envelope|disclaimer"}`, `gemini_arranger_rejections_total{reason="mid_value_truncation|schema_invalid|undeclared_tool"}` and `gemini_refusal_retries_total`.
  - Wired schema rejections, truncated mutations, arranger repairs and refusal retries into the new counters.
- **Session Integration (`api/session_transcripts.go`, `api/chat_handler.go`)**:
  - Each `sessionTurnEntry` now owns a `*DoomLoopTracker`; `getOrCreateSessionTracker` and `recordSessionToolCalls` expose thread-safe access from the chat pipeline, and the tracker survives every `commitSessionTurn`.
  - The streaming and non-streaming paths check the tracker before calling upstream, inject warnings at maximum recency, and return an explicit HTTP 429 `doom_loop_error` when the circuit breaker trips.
  - Emitted tool calls (after schema validation and Write-Serial throttling) are recorded for the next turn.
- **Regression Tests**:
  - `api/doom_loop_test.go`: identical repeats, A/B and A/B/C cycles, passive reads, mutation resets, canonical fingerprints, reset, new-user-turn detection and concurrent access.
  - `api/observation_masker_test.go`: size and turn triggers, recent-result preservation, UTF-8 safety, role preservation, overhead accounting and non-mutation of the input.
  - `api/session_tracker_test.go`: tracker persistence across commits and tool-call recording.
  - `api/metrics_test.go` and `gemini/arranger_test.go`: agentic counter exposition/increments and arranger repair reporting.

### Changed
- **Arranger (`gemini/arranger.go`)**:
  - Exported `IsMutatingTool` for API-layer bookkeeping.
  - `ArrangedOutput` now reports the repair kinds applied (`disclaimer`, `control_chars`, `brace_envelope`) so the API can expose them as metrics.

---

## [3.4.6] - 2026-11-22

### Added
- **Formal JSON Schema Validation (`gemini/schema_validator.go`)**:
  - Added a pure-Go JSON Schema validator (Draft 2020-12 / Draft-07 / Draft-04) backed by `github.com/santhosh-tekuri/jsonschema/v6 v6.0.3`.
  - Added `ToolSchemaRegistry`, a thread-safe LRU cache keyed by the SHA-256 hash of the request's tool block, plus `CompiledToolCatalog.ValidateCall` returning precise, model-actionable violations (`missing required property 'path'`, `type mismatch for 'path'`, undeclared tool, invalid JSON arguments).
  - Added `HasInvalidSchemaToolCall(raw, tools)` so the API layer can detect invalid calls before any mutation reaches OpenCode or the host.
- **Write-Serial Mutation Throttling (`gemini/arranger.go`)**:
  - Added `ThrottleMutatingToolCalls`, preserving every read-only call (`read`, `grep`, `glob`, `list`) while allowing at most one mutation (`write`, `edit`, `shell`, `patch`, `multiedit`, `apply_patch`) per turn. The batch is truncated at the first subsequent mutation so the next change can be verified before execution.
- **Abstract-Error Retry Protocol (`api/chat_handler.go`, `api/session_transcripts.go`)**:
  - Streaming and non-streaming paths now detect schema-invalid tool calls, reset the poisoned upstream conversation, and retry once with a targeted `[SYSTEM DIRECTIVE: Previous tool call failed validation: ...]` notice instead of replaying the broken raw tokens.
  - Added `isSchemaInvalidOutput` and `formatSchemaRetryDirective`; exhausted retries return an explicit SSE error / HTTP 502 instead of silently emitting a `stop` response.
- **Compact Tool Catalog (`gemini/arranger.go`)**:
  - `FormatToolsInstruction` now serializes a compact projection of each tool (`name`, truncated description, `required`, `properties`) instead of the full indented OpenAI envelope.
  - `required` is highlighted before `properties`, and `path` is forced to the first key in every relevant schema and required list, reducing prompt footprint and lost-in-the-middle drift.
- **Regression Tests**:
  - `gemini/schema_validator_test.go`: valid OpenCode tools (`write`, `read`, `edit`, `shell`, `list`), missing required key, type mismatch, undeclared tool, malformed JSON, `additionalProperties`, Draft-07, LRU eviction and concurrent cache access.
  - `gemini/arranger_test.go`: schema-valid/invalid arrangement, partial rejection, write throttling, parallel reads and compact-catalog size/order assertions.
  - `api/session_scope_test.go`: `isSchemaInvalidOutput` and abstract retry directive coverage.

### Changed
- **Dependencies (`go.mod`)**:
  - Added direct dependency `github.com/santhosh-tekuri/jsonschema/v6 v6.0.3` (pure Go, no CGO).
  - Bumped transitive `github.com/dlclark/regexp2` to `v1.11.0` as required by the validator.
- **Tool-Call Pipeline**:
  - `ArrangeGeminiOutputWithTools` is now the primary arranger entry point; `ArrangeGeminiOutput` remains as a backward-compatible wrapper with validation disabled.
  - Tagged, fenced and raw-JSON fallbacks all enforce schema validation when request tools are provided, and invalid calls are removed from both the tool-call list and the cleaned text.

---

## [3.4.5] - 2026-11-22

### Added
- **OpenAI SSE Tool-Call Index (`api/types.go`, `api/chat_handler.go`)**:
  - Added an optional `index` field to `OpenAIToolCall` and assigned sequential indexes (`0`, `1`, …) to every `tool_calls` delta, restoring strict compatibility with OpenCode and the Vercel AI SDK (`AI_TypeValidationError` when `tool_calls[].index` is missing).
  - Added `api/types_test.go` covering stream serialization (`"index":0`) and omission of the field from complete `chat.completion` responses.
- **Regression Tests (`gemini/arranger_test.go`)**:
  - Added `TestFindUpstreamDisclaimerOffset_UTF8FrenchApostrophes` verifying byte-exact offsets on French text containing many typographic apostrophes (`’`).
  - Added `TestArrangeGeminiOutput_SafeEnvelopeAutoClose` verifying that a write whose string values are complete is still repaired and executed.
  - Added `TestIsUnusableModelOutput_TruncatedMutation` (`api/session_scope_test.go`) validating retry detection for truncated writes and confirming truncated reads are not over-classified.
  - Extended `TestAutoCloseJSONBraces` with explicit `isMidValue` expectations, including dangling `:` and `,` separators.

### Fixed
- **Mid-Value Truncation Guard (`gemini/arranger.go`)**:
  - `autoCloseJSONBraces` now returns `(repaired string, isMidValue bool)`, flagging cutoffs inside a JSON string (`inStr == true`) or immediately after a pending `:` / `,` separator.
  - `extractTaggedToolCalls` and `findFirstJSONToolCall` refuse auto-repair for mutating tools (`write`, `edit`, `shell`, `patch`, `multiedit`, `apply_patch`) when `isMidValue` is true. A truncated write can no longer be turned into a plausible but incomplete file overwrite.
  - Added `HasTruncatedMutationToolCall`, wired into `isUnusableModelOutput` (`api/session_transcripts.go`) and both chat paths (`api/chat_handler.go`): truncated mutations trigger a fresh-conversation retry, then an explicit error instead of a silent `stop`.
  - Updated `TestArrangeGeminiOutput_TruncatedToolCallEOF` to assert rejection rather than truncated execution.
- **UTF-8 Disclaimer Offset (`gemini/arranger.go`)**:
  - Replaced the byte-shrinking `strings.ReplaceAll(s, "’", "'")` normalization with precompiled case-insensitive regexes matching `['’]` directly on the original bytes, eliminating offset drift on French reports.
- **Strict Tool Arguments (`gemini/arranger.go`)**:
  - Removed the silent `argsStr = "{}"` coercion; an explicitly supplied but malformed argument payload now returns `ToolCall{}, false` instead of reaching the host with missing required fields.
- **Pink Elephant Prompt Removal (`gemini/arranger.go`, `api/chat_handler.go`)**:
  - Removed the verbatim “0 remaining” / “Disregard any…” quota wording from the system prompt and both internal retry directives; replaced it with positive, deterministic tool-continuity statements.
  - Rule 3 now prescribes bounded parallel reads (2–4) and strictly serialized mutations (one file-writing call per turn); Rule 4 adds the Metadata-First pattern (`path` before `content` / `old_string` / `new_string`).

### Changed
- **Documentation Reconciliation (`audit-2026-11/RAPPORT_POST_AUDIT.md`)**:
  - Rewrote the Section 2 integration matrix to match `REGISTRE_CVSS.md` exactly: real components, CVSS scores, severities and post-audit statuses for F-01 to F-25, removing the fictitious `api/middleware.go`, `gemini/ws.go`, `api/server.go`, `api/stream.go` and related entries.

---

## [3.4.4] - 2026-11-22

### Added
- **Unified Local Quality Runner (`scripts/audit_check.sh`)**:
  - Added standalone root bash script (`scripts/audit_check.sh`) compensating for the removal of `.github/` workflows.
  - Automatically runs Go code formatting verification (`gofmt`), static analysis (`go vet` via Docker `golang:1.26-alpine`), and unit test suite execution with statement coverage calculation (`go test -short -cover ./...`), exiting with code 0 on clean runs.
- **Post-Implementation Audit & Integration Validation (`audit-2026-11/RAPPORT_POST_AUDIT.md`)**:
  - Published comprehensive post-implementation audit report evaluating the remediation of all 25 security and robustness findings (F-01 to F-25) from the November 2026 audit.
  - Validated progression of Global Maturity Index from **38/100 (Level 2/5 — NO-GO)** to **74.25/100 (Level 4/5 — GO Local-First / IDE Validated)** across all 6 maturity axes.
  - Updated CVSS registry ([REGISTRE_CVSS.md](audit-2026-11/REGISTRE_CVSS.md), [REGISTRE_CVSS.csv](audit-2026-11/REGISTRE_CVSS.csv)) and audit summary ([audit-2026-11/README.md](audit-2026-11/README.md)) with explicit post-audit remediation statuses.
- **Agent Workflow Insights on Long-Horizon Multi-Turn Tasks**:
  - Documented empirical performance differentials between `gemini-4-neon` and `gemini-4-hydrogen-task` when executing complex multi-turn plans:
    - `gemini-4-neon` exhibits zero vulnerability hallucination, correct path resolution, and accurate test interpretation, but can encounter cognitive saturation on mega-turns (omitting keys like `path`) or succumb to read-only confirmation bias when resuming partial tasks.
    - `gemini-4-hydrogen-task` executes rapidly but suffers from directory drift and hallucinated CVE identifiers when operating without strict constraints.

### Fixed
- **Upstream Safety Disclaimer Truncation & Automatic JSON Repair (`gemini/arranger.go`)**:
  - Implemented `autoCloseJSONBraces` and `findUpstreamDisclaimerOffset` in `gemini/arranger.go` to immunize the proxy engine against tool call truncations caused by Google's upstream web safety filters.
  - When safety classifiers trigger at the end of large write payloads, omitting the closing `}` or `</tool_call>` and appending canned refusals (*"I'm just a language model and can't help with that"*), the arranger now isolates the unclosed JSON, automatically balances unclosed braces and string quotes, extracts the tool call, and swallows the disclaimer from `CleanText`.
  - Added unit test suite in `gemini/arranger_test.go` (`TestArrangeGeminiOutput_TruncatedToolCallWithUpstreamDisclaimer`, `TestArrangeGeminiOutput_TruncatedToolCallEOF`, `TestAutoCloseJSONBraces`).

---

## [3.4.3] - 2026-11-22

### Changed
- **Local-First Repository Initialization & Remote Setup**:
  - Initialized Git repository on `main` branch connected to remote `git@github.com:ki2pixel/free-gemini-api.git`.
  - Hardened root and subfolder `.gitignore` rules aligning with security finding F-21: explicitly ignored binary model blob `*.cact` (`tuned.cact`), prompt debug traces (`**/debug_*.txt`), local agent session dumps (`debug/`), test coverage (`coverage.out`), and scan outputs (`results.sarif`).
  - Cleaned up obsolete local files (`task-209-res.txt` and legacy draft `pr_body.md`).

### Removed
- **Automated Cloud CI/CD & Dependabot Workflows**:
  - Removed `.github/` directory (`.github/workflows/ci.yml`, `.github/workflows/docker-publish.yml`, `.github/dependabot.yml`) to optimize the repository for private, local-first execution.
  - Eliminated high GitHub Actions quota consumption from multi-architecture Docker image builds (`amd64`/`arm64`).
  - Closed 10 automated Dependabot PRs and pruned remote dependency branches.

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
