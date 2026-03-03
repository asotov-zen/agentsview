package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runZencoderParserTest(
	t *testing.T, content string,
) (*ParsedSession, []ParsedMessage, error) {
	t.Helper()
	path := createTestFile(t, "test-uuid.jsonl", content)
	return ParseZencoderSession(path, "local")
}

func TestParseZencoderSession_Basic(t *testing.T) {
	header := `{"id":"abc-123","chatId":"chat-1","modelId":"model-1","parentId":"","creationReason":"newChat","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z","version":"1"}`
	system := `{"role":"system","content":"You are an AI assistant.\n\n# Environment\n\nWorking directory: /home/user/myproject\n\nOS: linux"}`
	user := `{"role":"user","content":[{"type":"text","text":"Fix the bug.","tag":"user-input"}]}`
	assistant := `{"role":"assistant","content":[{"type":"text","text":"Sure, I will fix it."}]}`
	finish := `{"role":"finish","reason":"endTurn"}`

	content := strings.Join([]string{
		header, system, user, assistant, finish,
	}, "\n")

	sess, msgs, err := runZencoderParserTest(t, content)
	require.NoError(t, err)
	require.NotNil(t, sess)

	assertSessionMeta(t, sess,
		"zencoder:abc-123",
		"myproject", AgentZencoder,
	)

	assert.Equal(t, "Fix the bug.", sess.FirstMessage)
	assertMessageCount(t, sess.MessageCount, 2)
	assert.Equal(t, 1, sess.UserMessageCount)

	wantStart := mustParseTime(t, "2024-01-01T00:00:00Z")
	assertTimestamp(t, sess.StartedAt, wantStart)

	wantEnd := mustParseTime(t, "2024-01-01T00:01:00Z")
	assertTimestamp(t, sess.EndedAt, wantEnd)

	require.Equal(t, 2, len(msgs))
	assertMessage(t, msgs[0], RoleUser, "Fix the bug.")
	assertMessage(t, msgs[1], RoleAssistant, "Sure, I will fix it.")
	assert.Equal(t, 0, msgs[0].Ordinal)
	assert.Equal(t, 1, msgs[1].Ordinal)

	// No parent → no relationship.
	assert.Empty(t, sess.ParentSessionID)
	assert.Equal(t, RelNone, sess.RelationshipType)
}

func TestParseZencoderSession_ToolCallAndReasoning(t *testing.T) {
	header := `{"id":"tc-123","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z"}`
	user := `{"role":"user","content":[{"type":"text","text":"Read the file."}]}`
	assistant := `{"role":"assistant","content":[` +
		`{"type":"reasoning","text":"Let me think about this.","provider":"anthropic","subtype":"thinking"},` +
		`{"type":"text","text":"I will read it now."},` +
		`{"type":"tool-call","toolCallId":"tc1","toolName":"Read","input":{"file_path":"main.go"}}` +
		`]}`

	content := strings.Join([]string{
		header, user, assistant,
	}, "\n")

	sess, msgs, err := runZencoderParserTest(t, content)
	require.NoError(t, err)
	require.NotNil(t, sess)

	assertMessageCount(t, sess.MessageCount, 2)
	assert.Equal(t, 1, sess.UserMessageCount)

	assert.False(t, msgs[0].HasThinking)
	assert.False(t, msgs[0].HasToolUse)

	assert.True(t, msgs[1].HasThinking)
	assert.True(t, msgs[1].HasToolUse)
	assert.Contains(t, msgs[1].Content, "[Thinking]")
	assert.Contains(t, msgs[1].Content, "Let me think about this.")
	assert.Contains(t, msgs[1].Content, "[Read: main.go]")

	require.Equal(t, 1, len(msgs[1].ToolCalls))
	assert.Equal(t, "Read", msgs[1].ToolCalls[0].ToolName)
	assert.Equal(t, "Read", msgs[1].ToolCalls[0].Category)
	assert.Equal(t, "tc1", msgs[1].ToolCalls[0].ToolUseID)
}

func TestParseZencoderSession_ToolResults(t *testing.T) {
	header := `{"id":"tr-123","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z"}`
	user := `{"role":"user","content":[{"type":"text","text":"Read it."}]}`
	assistant := `{"role":"assistant","content":[` +
		`{"type":"tool-call","toolCallId":"tc1","toolName":"Read","input":{"file_path":"main.go"}}` +
		`]}`
	tool := `{"role":"tool","content":[` +
		`{"type":"tool-result","toolCallId":"tc1","toolName":"Read","content":[{"type":"text","text":"package main"}],"isError":false}` +
		`]}`

	content := strings.Join([]string{
		header, user, assistant, tool,
	}, "\n")

	sess, msgs, err := runZencoderParserTest(t, content)
	require.NoError(t, err)
	require.NotNil(t, sess)

	assertMessageCount(t, sess.MessageCount, 3)

	// Tool result is emitted as RoleUser message.
	assert.Equal(t, RoleUser, msgs[2].Role)
	require.Equal(t, 1, len(msgs[2].ToolResults))
	assert.Equal(t, "tc1", msgs[2].ToolResults[0].ToolUseID)
	assert.Equal(t, len("package main"),
		msgs[2].ToolResults[0].ContentLength)
}

func TestParseZencoderSession_UserInputTagFiltering(t *testing.T) {
	header := `{"id":"tag-123","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z"}`
	user := `{"role":"user","content":[` +
		`{"type":"text","text":"system instructions","tag":"instructions"},` +
		`{"type":"text","text":"actual user input","tag":"user-input"},` +
		`{"type":"text","text":"todo reminder","tag":"todo-reminder"}` +
		`]}`
	assistant := `{"role":"assistant","content":[{"type":"text","text":"Got it."}]}`

	content := strings.Join([]string{
		header, user, assistant,
	}, "\n")

	sess, msgs, err := runZencoderParserTest(t, content)
	require.NoError(t, err)
	require.NotNil(t, sess)

	// Only "user-input" tagged content should be extracted.
	assert.Equal(t, "actual user input", msgs[0].Content)
	assert.NotContains(t, msgs[0].Content, "system instructions")
	assert.NotContains(t, msgs[0].Content, "todo reminder")
}

func TestParseZencoderSession_DirectContinuation(t *testing.T) {
	header := `{"id":"child-123","parentId":"parent-456","creationReason":"directContinuation","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z"}`
	user := `{"role":"user","content":[{"type":"text","text":"Continue."}]}`
	assistant := `{"role":"assistant","content":[{"type":"text","text":"Continuing."}]}`

	content := strings.Join([]string{
		header, user, assistant,
	}, "\n")

	sess, _, err := runZencoderParserTest(t, content)
	require.NoError(t, err)
	require.NotNil(t, sess)

	assert.Equal(t, "zencoder:parent-456", sess.ParentSessionID)
	assert.Equal(t, RelContinuation, sess.RelationshipType)
}

func TestParseZencoderSession_SummarizedContinuation(t *testing.T) {
	header := `{"id":"child-789","parentId":"parent-012","creationReason":"summarizedContinuation","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z"}`
	user := `{"role":"user","content":[{"type":"text","text":"Continue."}]}`
	assistant := `{"role":"assistant","content":[{"type":"text","text":"OK."}]}`

	content := strings.Join([]string{
		header, user, assistant,
	}, "\n")

	sess, _, err := runZencoderParserTest(t, content)
	require.NoError(t, err)
	require.NotNil(t, sess)

	assert.Equal(t, "zencoder:parent-012", sess.ParentSessionID)
	assert.Equal(t, RelContinuation, sess.RelationshipType)
}

func TestParseZencoderSession_ProjectExtraction(t *testing.T) {
	header := `{"id":"proj-123","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z"}`
	system := `{"role":"system","content":"You are helpful.\n\nWorking directory: /home/user/workspace/coolproject\n"}`
	user := `{"role":"user","content":[{"type":"text","text":"hello"}]}`

	content := strings.Join([]string{
		header, system, user,
	}, "\n")

	sess, _, err := runZencoderParserTest(t, content)
	require.NoError(t, err)
	require.NotNil(t, sess)

	assert.Equal(t, "coolproject", sess.Project)
}

func TestParseZencoderSession_EmptySession(t *testing.T) {
	// Header only, no messages.
	header := `{"id":"empty-123","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z"}`

	sess, msgs, err := runZencoderParserTest(t, header)
	require.NoError(t, err)
	assert.Nil(t, sess)
	assert.Nil(t, msgs)
}

func TestParseZencoderSession_PermissionAndFinishSkipped(t *testing.T) {
	header := `{"id":"skip-123","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z"}`
	user := `{"role":"user","content":[{"type":"text","text":"Do it."}]}`
	permission := `{"role":"permission","data":{"allowed":true}}`
	assistant := `{"role":"assistant","content":[{"type":"text","text":"Done."}]}`
	finish := `{"role":"finish","reason":"endTurn"}`

	content := strings.Join([]string{
		header, user, permission, assistant, finish,
	}, "\n")

	sess, msgs, err := runZencoderParserTest(t, content)
	require.NoError(t, err)
	require.NotNil(t, sess)

	// permission and finish are skipped, only user + assistant.
	assertMessageCount(t, sess.MessageCount, 2)
	require.Equal(t, 2, len(msgs))
}

func TestParseZencoderSession_FirstMessageTruncation(t *testing.T) {
	header := `{"id":"trunc-123","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z"}`
	longText := strings.Repeat("a", 400)
	user := `{"role":"user","content":[{"type":"text","text":"` + longText + `"}]}`

	content := strings.Join([]string{header, user}, "\n")

	sess, _, err := runZencoderParserTest(t, content)
	require.NoError(t, err)
	require.NotNil(t, sess)
	// truncate clips at 300 chars + 3 ellipsis chars = 303.
	assert.Equal(t, 303, len(sess.FirstMessage))
}

func TestParseZencoderSession_MissingFile(t *testing.T) {
	sess, msgs, err := ParseZencoderSession(
		"/nonexistent/test.jsonl", "local",
	)
	require.NoError(t, err)
	assert.Nil(t, sess)
	assert.Nil(t, msgs)
}

func TestParseZencoderSession_FallbackSessionID(t *testing.T) {
	// Header with no id field → falls back to filename.
	header := `{"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z"}`
	user := `{"role":"user","content":[{"type":"text","text":"hello"}]}`

	content := strings.Join([]string{header, user}, "\n")

	sess, _, err := runZencoderParserTest(t, content)
	require.NoError(t, err)
	require.NotNil(t, sess)
	// Falls back to filename-derived ID.
	assert.Equal(t, "zencoder:test-uuid", sess.ID)
}

func TestDiscoverZencoderSessions(t *testing.T) {
	dir := t.TempDir()

	// Create some session files.
	for _, name := range []string{
		"abc-123.jsonl",
		"def-456.jsonl",
		"not-jsonl.txt",
	} {
		f, err := os.Create(filepath.Join(dir, name))
		require.NoError(t, err)
		f.Close()
	}

	// Create a subdirectory (should be skipped).
	require.NoError(t, os.Mkdir(
		filepath.Join(dir, "subdir"), 0o755,
	))

	files := DiscoverZencoderSessions(dir)
	assert.Equal(t, 2, len(files))
	for _, f := range files {
		assert.Equal(t, AgentZencoder, f.Agent)
		assert.True(t, strings.HasSuffix(f.Path, ".jsonl"))
	}
}

func TestDiscoverZencoderSessions_EmptyDir(t *testing.T) {
	files := DiscoverZencoderSessions("")
	assert.Nil(t, files)
}

func TestFindZencoderSourceFile(t *testing.T) {
	dir := t.TempDir()
	name := "abc-def-123.jsonl"
	f, err := os.Create(filepath.Join(dir, name))
	require.NoError(t, err)
	f.Close()

	result := FindZencoderSourceFile(dir, "abc-def-123")
	assert.Equal(t, filepath.Join(dir, name), result)

	// Non-existent ID.
	result = FindZencoderSourceFile(dir, "nonexistent")
	assert.Empty(t, result)

	// Empty dir.
	result = FindZencoderSourceFile("", "abc-def-123")
	assert.Empty(t, result)
}

func TestParseZencoderSession_UserContentWithoutTag(t *testing.T) {
	header := `{"id":"notag-123","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z"}`
	user := `{"role":"user","content":[{"type":"text","text":"no tag input"}]}`
	assistant := `{"role":"assistant","content":[{"type":"text","text":"OK."}]}`

	content := strings.Join([]string{
		header, user, assistant,
	}, "\n")

	sess, msgs, err := runZencoderParserTest(t, content)
	require.NoError(t, err)
	require.NotNil(t, sess)

	// Content without tag should be included.
	assert.Equal(t, "no tag input", msgs[0].Content)
}

func TestParseZencoderSession_NewChatNoRelationship(t *testing.T) {
	header := `{"id":"new-123","parentId":"some-parent","creationReason":"newChat","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:01:00Z"}`
	user := `{"role":"user","content":[{"type":"text","text":"hello"}]}`

	content := strings.Join([]string{header, user}, "\n")

	sess, _, err := runZencoderParserTest(t, content)
	require.NoError(t, err)
	require.NotNil(t, sess)

	// newChat even with parentId → no relationship.
	assert.Empty(t, sess.ParentSessionID)
	assert.Equal(t, RelNone, sess.RelationshipType)
}

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts := parseTimestamp(s)
	if ts.IsZero() {
		t.Fatalf("failed to parse timestamp %q", s)
	}
	return ts
}
