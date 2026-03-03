package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// zencoderCwdRe extracts the working directory from a Zencoder
// system message's environment block.
var zencoderCwdRe = regexp.MustCompile(
	`Working directory:\s+(.+)`,
)

// zencoderSessionIDRe extracts a subagent session ID from a
// tool-result text block (e.g. "<session-id>uuid</session-id>").
var zencoderSessionIDRe = regexp.MustCompile(
	`<session-id>([^<]+)</session-id>`,
)

// zencoderSessionBuilder accumulates state while scanning a
// Zencoder JSONL session file line by line.
type zencoderSessionBuilder struct {
	messages        []ParsedMessage
	subagentMap     map[string]string // toolCallId → "zencoder:<session-id>"
	firstMessage    string
	startedAt       time.Time
	endedAt         time.Time
	sessionID       string
	parentID        string
	creationReason  string
	project         string
	ordinal         int
	headerProcessed bool
}

func newZencoderSessionBuilder() *zencoderSessionBuilder {
	return &zencoderSessionBuilder{
		subagentMap: map[string]string{},
		project:     "unknown",
	}
}

// processHeader handles the first line of a Zencoder JSONL file.
func (b *zencoderSessionBuilder) processHeader(line string) {
	b.headerProcessed = true
	b.sessionID = gjson.Get(line, "id").Str
	b.parentID = gjson.Get(line, "parentId").Str
	b.creationReason = gjson.Get(line, "creationReason").Str

	createdAt := gjson.Get(line, "createdAt").Str
	if ts := parseTimestamp(createdAt); !ts.IsZero() {
		b.startedAt = ts
	}
	updatedAt := gjson.Get(line, "updatedAt").Str
	if ts := parseTimestamp(updatedAt); !ts.IsZero() {
		b.endedAt = ts
	}
}

// processMessage handles lines 2+ (messages) in a Zencoder
// JSONL file.
func (b *zencoderSessionBuilder) processMessage(line string) {
	role := gjson.Get(line, "role").Str

	switch role {
	case "system":
		b.handleSystemMessage(line)
	case "user":
		b.handleUserMessage(line)
	case "assistant":
		b.handleAssistantMessage(line)
	case "tool":
		b.handleToolMessage(line)
		// "finish", "permission" — skip entirely.
	}
}

func (b *zencoderSessionBuilder) handleSystemMessage(
	line string,
) {
	// System content is a plain string (not array).
	content := gjson.Get(line, "content").Str
	if content == "" {
		return
	}

	// Extract project from "Working directory: /path".
	if m := zencoderCwdRe.FindStringSubmatch(content); len(m) > 1 {
		cwd := strings.TrimSpace(m[1])
		if proj := ExtractProjectFromCwd(cwd); proj != "" {
			b.project = proj
		}
	}
}

func (b *zencoderSessionBuilder) handleUserMessage(
	line string,
) {
	content := extractZencoderUserContent(
		gjson.Get(line, "content"),
	)
	if strings.TrimSpace(content) == "" {
		return
	}

	if b.firstMessage == "" {
		b.firstMessage = truncate(
			strings.ReplaceAll(content, "\n", " "), 300,
		)
	}

	b.messages = append(b.messages, ParsedMessage{
		Ordinal:       b.ordinal,
		Role:          RoleUser,
		Content:       content,
		ContentLength: len(content),
	})
	b.ordinal++
}

func (b *zencoderSessionBuilder) handleAssistantMessage(
	line string,
) {
	content, hasThinking, hasToolUse, tcs :=
		extractZencoderAssistantContent(
			gjson.Get(line, "content"),
		)

	if strings.TrimSpace(content) == "" && !hasToolUse {
		return
	}

	b.messages = append(b.messages, ParsedMessage{
		Ordinal:       b.ordinal,
		Role:          RoleAssistant,
		Content:       content,
		HasThinking:   hasThinking,
		HasToolUse:    hasToolUse,
		ContentLength: len(content),
		ToolCalls:     tcs,
	})
	b.ordinal++
}

func (b *zencoderSessionBuilder) handleToolMessage(
	line string,
) {
	var toolResults []ParsedToolResult
	gjson.Get(line, "content").ForEach(
		func(_, block gjson.Result) bool {
			if block.Get("type").Str != "tool-result" {
				return true
			}
			toolCallID := block.Get("toolCallId").Str
			if toolCallID == "" {
				return true
			}
			cl := zencoderToolResultContentLength(
				block.Get("content"),
			)
			toolResults = append(toolResults, ParsedToolResult{
				ToolUseID:     toolCallID,
				ContentLength: cl,
			})

			// Extract <session-id> from tool-result content
			// to map subagent tool calls to their sessions.
			block.Get("content").ForEach(
				func(_, cb gjson.Result) bool {
					if m := zencoderSessionIDRe.FindStringSubmatch(
						cb.Get("text").Str,
					); len(m) > 1 {
						b.subagentMap[toolCallID] =
							"zencoder:" + m[1]
					}
					return true
				},
			)

			return true
		},
	)

	if len(toolResults) == 0 {
		return
	}

	totalLen := 0
	for _, tr := range toolResults {
		totalLen += tr.ContentLength
	}

	b.messages = append(b.messages, ParsedMessage{
		Ordinal:       b.ordinal,
		Role:          RoleUser,
		ContentLength: totalLen,
		ToolResults:   toolResults,
	})
	b.ordinal++
}

// extractZencoderUserContent extracts text from a Zencoder user
// message content array, filtering by tag. Only includes blocks
// where tag is "user-input" or absent.
func extractZencoderUserContent(
	content gjson.Result,
) string {
	if !content.IsArray() {
		return ""
	}

	var parts []string
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").Str != "text" {
			return true
		}
		tag := block.Get("tag").Str
		if tag != "" && tag != "user-input" {
			return true
		}
		if text := block.Get("text").Str; text != "" {
			parts = append(parts, text)
		}
		return true
	})
	return strings.Join(parts, "\n")
}

// extractZencoderAssistantContent extracts text, thinking, and
// tool calls from a Zencoder assistant message content array.
func extractZencoderAssistantContent(
	content gjson.Result,
) (string, bool, bool, []ParsedToolCall) {
	if !content.IsArray() {
		return "", false, false, nil
	}

	var (
		parts       []string
		toolCalls   []ParsedToolCall
		hasThinking bool
		hasToolUse  bool
	)
	content.ForEach(func(_, block gjson.Result) bool {
		switch block.Get("type").Str {
		case "text":
			if text := block.Get("text").Str; text != "" {
				parts = append(parts, text)
			}
		case "reasoning":
			if text := block.Get("text").Str; text != "" {
				hasThinking = true
				parts = append(parts,
					"[Thinking]\n"+text+"\n[/Thinking]")
			}
		case "tool-call":
			hasToolUse = true
			name := block.Get("toolName").Str
			if name == "" {
				return true
			}
			tc := ParsedToolCall{
				ToolUseID: block.Get("toolCallId").Str,
				ToolName:  name,
				Category:  NormalizeToolCategory(name),
				InputJSON: block.Get("input").Raw,
			}
			toolCalls = append(toolCalls, tc)
			// Synthesize a Claude-compatible JSON block for
			// formatToolUse, which expects "name" and "input".
			synth := fmt.Sprintf(
				`{"name":%q,"input":%s}`,
				name,
				orDefault(block.Get("input").Raw, "{}"),
			)
			parts = append(parts,
				formatToolUse(gjson.Parse(synth)))
		}
		return true
	})

	return strings.Join(parts, "\n"),
		hasThinking, hasToolUse, toolCalls
}

// zencoderToolResultContentLength computes the total text
// length from a tool-result's content array.
func zencoderToolResultContentLength(
	content gjson.Result,
) int {
	if !content.IsArray() {
		return 0
	}
	total := 0
	content.ForEach(func(_, block gjson.Result) bool {
		// Various content types: text, text-file-chunk,
		// shell-result, etc. all may have a "text" field.
		total += len(block.Get("text").Str)
		return true
	})
	return total
}

// ParseZencoderSession parses a Zencoder JSONL session file.
// Returns (nil, nil, nil) if the file doesn't exist or
// contains no user/assistant messages.
func ParseZencoderSession(
	path, machine string,
) (*ParsedSession, []ParsedMessage, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("stat %s: %w", path, err)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	lr := newLineReader(f, maxLineSize)
	b := newZencoderSessionBuilder()

	lineNum := 0
	for {
		line, ok := lr.next()
		if !ok {
			break
		}
		if !gjson.Valid(line) {
			lineNum++
			continue
		}

		if lineNum == 0 {
			b.processHeader(line)
		} else {
			b.processMessage(line)
		}
		lineNum++
	}

	if err := lr.Err(); err != nil {
		return nil, nil,
			fmt.Errorf("reading zencoder %s: %w", path, err)
	}

	// Filter: require at least one user or assistant message.
	hasContent := false
	for _, m := range b.messages {
		if m.Content != "" {
			hasContent = true
			break
		}
	}
	if !hasContent {
		return nil, nil, nil
	}

	sessionID := b.sessionID
	if sessionID == "" {
		sessionID = strings.TrimSuffix(
			filepath.Base(path), ".jsonl",
		)
	}
	sessionID = "zencoder:" + sessionID

	// Map creationReason to RelationshipType.
	var relType RelationshipType
	var parentSessionID string
	if b.parentID != "" {
		switch b.creationReason {
		case "directContinuation",
			"summarizedContinuation",
			"summarizationRequest":
			relType = RelContinuation
			parentSessionID = "zencoder:" + b.parentID
		default:
			// "newChat" or unknown → no relationship.
		}
	}

	// Annotate tool calls with their subagent session IDs.
	annotateSubagentSessions(b.messages, b.subagentMap)

	userCount := 0
	for _, m := range b.messages {
		if m.Role == RoleUser && m.Content != "" {
			userCount++
		}
	}

	sess := &ParsedSession{
		ID:               sessionID,
		Project:          b.project,
		Machine:          machine,
		Agent:            AgentZencoder,
		ParentSessionID:  parentSessionID,
		RelationshipType: relType,
		FirstMessage:     b.firstMessage,
		StartedAt:        b.startedAt,
		EndedAt:          b.endedAt,
		MessageCount:     len(b.messages),
		UserMessageCount: userCount,
		File: FileInfo{
			Path:  path,
			Size:  info.Size(),
			Mtime: info.ModTime().UnixNano(),
		},
	}

	return sess, b.messages, nil
}

// IsZencoderSessionFileName reports whether name matches
// the Zencoder session file pattern (*.jsonl).
func IsZencoderSessionFileName(name string) bool {
	return strings.HasSuffix(name, ".jsonl")
}

// DiscoverZencoderSessions finds all JSONL files under
// the Zencoder sessions directory (~/.zencoder/sessions/*.jsonl).
func DiscoverZencoderSessions(
	sessionsDir string,
) []DiscoveredFile {
	if sessionsDir == "" {
		return nil
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil
	}

	var files []DiscoveredFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !IsZencoderSessionFileName(name) {
			continue
		}
		files = append(files, DiscoveredFile{
			Path:  filepath.Join(sessionsDir, name),
			Agent: AgentZencoder,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files
}

// FindZencoderSourceFile locates a Zencoder session file by
// its raw session ID (without the "zencoder:" prefix).
func FindZencoderSourceFile(
	sessionsDir, rawID string,
) string {
	if sessionsDir == "" || !IsValidSessionID(rawID) {
		return ""
	}
	candidate := filepath.Join(sessionsDir, rawID+".jsonl")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}
