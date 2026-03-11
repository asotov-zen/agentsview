# Investigation: Missing Per-Message Timestamps for Zencoder Sessions

## Bug Summary

Zencoder session messages display "--" instead of actual date/time
timestamps in the session detail view. This affects every individual
message and tool call group in a Zencoder session. Session-level
timestamps (sidebar list, breadcrumb header) display correctly.

## Root Cause Analysis

The Zencoder parser (`internal/parser/zencoder.go`) extracts
session-level timestamps from the JSONL header line (`createdAt` and
`updatedAt`), but **never extracts per-message timestamps** from
individual message lines.

Each Zencoder JSONL message line contains a `createdAt` field:

```json
{"role":"user","content":[...],"createdAt":"2026-03-03T21:29:29.402Z"}
{"role":"assistant","content":[...],"createdAt":"2026-03-03T21:29:34.492Z"}
{"role":"tool","content":[...],"createdAt":"2026-03-03T21:29:34.512Z"}
```

However, when the parser creates `ParsedMessage` structs, it never
sets the `Timestamp` field. The `Timestamp` field defaults to Go's
zero `time.Time`, which the sync engine converts to an empty string
via `timeutil.Format()`. The frontend's `formatTimestamp()` then
returns "--" for empty strings.

### Data Flow

```
Zencoder JSONL:  {"role":"user", ..., "createdAt":"2026-03-03T21:29:29.402Z"}
                                                    |
Parser (zencoder.go):  ParsedMessage{..., Timestamp: time.Time{}}  <-- NOT SET
                                                    |
Sync (engine.go:1793): db.Message{..., Timestamp: ""}  <-- empty string
                                                    |
Frontend (format.ts:30): formatTimestamp("") -> "--"  <-- displayed as dash
```

### Comparison with Working Parsers

**Codex parser** (`internal/parser/codex.go`): Extracts `timestamp`
from each line at line 50-51 and passes it to every `ParsedMessage`
at line 126.

**Claude parser** (`internal/parser/claude.go`): Extracts timestamps
via `extractTimestamp()` at line 162 and sets them on each message at
line 497.

Both parsers set `ParsedMessage.Timestamp` for every message. The
Zencoder parser is the only one that omits this.

## Affected Components

| Component | File | Status |
|---|---|---|
| Zencoder parser | `internal/parser/zencoder.go` | Missing per-message timestamp extraction |
| Zencoder tests | `internal/parser/zencoder_test.go` | No assertions for message timestamps |
| Session-level timestamps | Same parser, `processHeader()` | Working correctly |
| Frontend display | `frontend/src/lib/utils/format.ts` | Working (shows "--" for null/empty) |
| DB storage | `internal/db/messages.go` | Working (stores empty string) |
| Sync engine | `internal/sync/engine.go:1793` | Working (converts zero time to "") |

## Proposed Solution

### 1. Extract `createdAt` in each `processMessage()` handler

In `zencoder.go`, at the start of `processMessage()` (or within each
handler), extract the `createdAt` field from the JSONL line and parse
it using `parseTimestamp()`. Pass the resulting `time.Time` to each
`ParsedMessage` struct's `Timestamp` field.

Specifically:

- In `processMessage()` (line 70), extract `createdAt` from the line
  and pass it to each handler method.
- Each handler (`handleSystemMessage`, `handleUserMessage`,
  `handleAssistantMessage`, `handleToolMessage`) should set
  `Timestamp` on every `ParsedMessage` it creates.
- Also update `startedAt`/`endedAt` bounds from message timestamps
  (as a secondary source if header timestamps are missing).

### 2. Update tests

Add test assertions in `zencoder_test.go` to verify that parsed
messages have correct `Timestamp` values extracted from the `createdAt`
field of each JSONL line.

### Edge Cases

- Lines without `createdAt`: Leave `Timestamp` as zero time (same
  behavior as other parsers when timestamps are missing).
- Header line: Already handled separately by `processHeader()`, no
  change needed.
- `finish` and `permission` lines: May or may not have `createdAt`.
  Extract if present.
- Multiple messages from a single handler call (e.g.,
  `handleUserMessage` creates both user and system messages): Use the
  same timestamp from the line for all messages created from it.

## Implementation Notes

### Changes Made

**`internal/parser/zencoder.go`**:
- `processMessage()`: Extract `createdAt` from each JSONL line via
  `parseTimestamp(gjson.Get(line, "createdAt").Str)` and pass the
  resulting `time.Time` to each handler method.
- `handleSystemMessage()`, `handleUserMessage()`,
  `handleAssistantMessage()`, `handleToolMessage()`: Updated
  signatures to accept `ts time.Time` and set `Timestamp` on every
  `ParsedMessage` struct they create.
- `finish` message handler: Also sets `Timestamp` from the line's
  `createdAt`.

**`internal/parser/zencoder_test.go`**:
- Added `TestParseZencoderSession_MessageTimestamps`: Verifies that
  each message type (system, user, assistant, tool result, finish)
  gets the correct timestamp from its JSONL line's `createdAt` field.
- Added `TestParseZencoderSession_MessageTimestamps_Missing`: Verifies
  that lines without `createdAt` produce zero-time timestamps (same
  behavior as other parsers for missing timestamps).

### Test Results

All 21 Zencoder parser tests pass. No regressions.
