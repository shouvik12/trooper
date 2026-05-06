package main

import (
	"testing"
)

// ── Classifier Tests ──────────────────────────────────────────────────────────

func TestIsSimpleTurn_SimplePatterns(t *testing.T) {
	cases := []struct {
		input    string
		expected bool
	}{
		{"how many days in a week", true},
		{"summarise what we have covered", false},
		{"summarize our discussion", false},
		{"what does EOF mean", true},
		{"define recursion", true},
		{"fix the grammar in this sentence", true},
		{"give me an example", true},
		{"remind me what we said", true},
		{"what have we covered", true},
		{"repeat that", true},
		{"abbreviation for EOF", true},
		{"calculate 2 plus 2", true},
		{"convert celsius to fahrenheit", true},
	}

	for _, c := range cases {
		result := isSimpleTurn(c.input)
		if result != c.expected {
			t.Errorf("isSimpleTurn(%q) = %v, want %v", c.input, result, c.expected)
		}
	}
}

func TestIsSimpleTurn_ComplexPatterns(t *testing.T) {
	cases := []struct {
		input    string
		expected bool
	}{
		{"explain why goroutines are better than threads", false},
		{"should i use redis or postgres for this", false},
		{"why does this fail in production", false},
		{"how do i implement a circuit breaker in Go", false},
		{"debug this code for me", false},
		{"what happens if the context is cancelled", false},
		{"for my use case which approach is better", false},
		{"walk me through the architecture", false},
		{"step by step how do i deploy this", false},
		{"based on what we said which is better", false},
		{"given our discussion what should i do", false},
		{"what causes a race condition", false},
		{"would you recommend using kafka here", false},
	}

	for _, c := range cases {
		result := isSimpleTurn(c.input)
		if result != c.expected {
			t.Errorf("isSimpleTurn(%q) = %v, want %v", c.input, result, c.expected)
		}
	}
}

func TestIsSimpleTurn_CodeDetection(t *testing.T) {
	cases := []struct {
		input    string
		expected bool
	}{
		{"fix this func main() { fmt.Println() }", false},
		{"what does ```go func main() {}``` do", false},
		{"class MyClass extends Base", false},
		{"import React from react", false},
		{"def hello(): print hi", false},
		{"package main import fmt", false},
		{"struct User { Name string }", false},
		{"const x == y + 1", false},
		{"return err != nil;", false},
	}

	for _, c := range cases {
		result := isSimpleTurn(c.input)
		if result != c.expected {
			t.Errorf("isSimpleTurn(%q) = %v, want %v", c.input, result, c.expected)
		}
	}
}

func TestIsSimpleTurn_LengthGuard(t *testing.T) {
	// Over 20 words → complex regardless of safe patterns
	long := "summarise what we have covered in our discussion about building a Go proxy called trooper that handles fallback routing with context preservation"
	result := isSimpleTurn(long)
	if result != false {
		t.Errorf("isSimpleTurn(long message) = %v, want false", result)
	}
}

func TestIsSimpleTurn_CharLengthGuard(t *testing.T) {
	// Over 300 chars → complex
	long := "summarise " + string(make([]byte, 295))
	result := isSimpleTurn(long)
	if result != false {
		t.Errorf("isSimpleTurn(300+ chars) = %v, want false", result)
	}
}

func TestIsSimpleTurn_Default(t *testing.T) {
	// Unknown message with no patterns → Claude (safety first)
	cases := []string{
		"trooper is great",
		"hello there",
		"interesting",
		"okay",
	}
	for _, c := range cases {
		result := isSimpleTurn(c)
		if result != false {
			t.Errorf("isSimpleTurn(%q) = %v, want false (default to Claude)", c, result)
		}
	}
}

// ── Word Count Tests ──────────────────────────────────────────────────────────

func TestWordCount(t *testing.T) {
	cases := []struct {
		input    string
		expected int
	}{
		{"hello world", 2},
		{"how many days in a week", 6},
		{"", 0},
		{"one", 1},
		{"  spaces   between   words  ", 3},
	}

	for _, c := range cases {
		result := wordCount(c.input)
		if result != c.expected {
			t.Errorf("wordCount(%q) = %v, want %v", c.input, result, c.expected)
		}
	}
}

// ── Code Detection Tests ──────────────────────────────────────────────────────

func TestContainsCode(t *testing.T) {
	cases := []struct {
		input    string
		expected bool
	}{
		{"func main() {}", true},
		{"```go code```", true},
		{"class Foo {}", true},
		{"import os", true},
		{"package main", true},
		{"struct User {}", true},
		{"def hello():", true},
		{"x == y", true},
		{"return err;", true},
		{"hello world", false},
		{"how many tokens", false},
		{"summarise our talk", false},
		{"what does EOF mean", false},
	}

	for _, c := range cases {
		result := containsCode(c.input)
		if result != c.expected {
			t.Errorf("containsCode(%q) = %v, want %v", c.input, result, c.expected)
		}
	}
}

// ── Token Estimation Tests ────────────────────────────────────────────────────

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		input    string
		expected int
	}{
		{"hell", 1},        // 4 chars = 1 token
		{"hello wor", 2},   // 9 chars = 2 tokens
		{"", 0},            // empty = 0
		{"hello world", 2}, // 11 chars = 2 tokens
	}

	for _, c := range cases {
		result := estimateTokens(c.input)
		if result != c.expected {
			t.Errorf("estimateTokens(%q) = %v, want %v", c.input, result, c.expected)
		}
	}
}

// ── Context Compaction Tests ──────────────────────────────────────────────────

func TestBuildContext_NoCompactionNeeded(t *testing.T) {
	history := []map[string]string{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "hi there"},
	}
	result := buildContext(history)
	if len(result) != len(history) {
		t.Errorf("buildContext() = %d messages, want %d", len(result), len(history))
	}
}

func TestBuildContext_EmptyHistory(t *testing.T) {
	result := buildContext([]map[string]string{})
	if len(result) != 0 {
		t.Errorf("buildContext(empty) = %d messages, want 0", len(result))
	}
}

func TestBuildContext_CompactionTriggered(t *testing.T) {
	history := []map[string]string{}
	for i := 0; i < 70; i++ {
		history = append(history, map[string]string{
			"role":    "user",
			"content": "this is a test message that is about one hundred tokens long and contains enough words to push the context window over the limit when repeated many times across multiple turns in a long conversation",
		})
		history = append(history, map[string]string{
			"role":    "assistant",
			"content": "this is a response message that is also about one hundred tokens long and contains enough words to contribute meaningfully to the total token count when accumulated across many turns in the session",
		})
	}

	result := buildContext(history)

	// Result should be smaller than input
	if len(result) >= len(history) {
		t.Errorf("buildContext() did not compact — result %d >= input %d", len(result), len(history))
	}

	// Anchor turns preserved verbatim
	if result[0]["content"] != history[0]["content"] {
		t.Errorf("buildContext() anchor turn 1 not preserved")
	}
	if result[1]["content"] != history[1]["content"] {
		t.Errorf("buildContext() anchor turn 2 not preserved")
	}

	// Should contain a SITREP system message
	hasSITREP := false
	for _, m := range result {
		if m["role"] == "system" {
			hasSITREP = true
			break
		}
	}
	if !hasSITREP {
		t.Errorf("buildContext() compaction did not produce a SITREP system message")
	}
}

func TestBuildContext_AnchorAlwaysPreserved(t *testing.T) {
	history := []map[string]string{
		{"role": "user", "content": "ANCHOR MESSAGE ONE — must always be preserved"},
		{"role": "assistant", "content": "ANCHOR MESSAGE TWO — must always be preserved"},
	}
	for i := 0; i < 100; i++ {
		history = append(history, map[string]string{
			"role":    "user",
			"content": "filler message to push context over the limit and trigger compaction logic in buildContext function across many turns",
		})
	}

	result := buildContext(history)

	if result[0]["content"] != "ANCHOR MESSAGE ONE — must always be preserved" {
		t.Errorf("buildContext() did not preserve anchor message 1")
	}
	if result[1]["content"] != "ANCHOR MESSAGE TWO — must always be preserved" {
		t.Errorf("buildContext() did not preserve anchor message 2")
	}
}

func TestBuildContext_TailPreserved(t *testing.T) {
	history := []map[string]string{}
	for i := 0; i < 70; i++ {
		history = append(history, map[string]string{
			"role":    "user",
			"content": "filler message to push context over the limit and trigger compaction logic in the buildContext function across turns",
		})
	}
	history = append(history, map[string]string{
		"role":    "user",
		"content": "TAIL MESSAGE — this must appear in the result",
	})

	result := buildContext(history)

	lastMessage := result[len(result)-1]
	if lastMessage["content"] != "TAIL MESSAGE — this must appear in the result" {
		t.Errorf("buildContext() did not preserve tail message, got: %s", lastMessage["content"])
	}
}

func TestBuildContext_SITREPFormat(t *testing.T) {
	history := []map[string]string{}
	for i := 0; i < 70; i++ {
		history = append(history, map[string]string{
			"role":    "user",
			"content": "building a go proxy called trooper that falls back to ollama when claude quota runs out with context preservation and session management across multiple providers including claude gemini and openai",
		})
		history = append(history, map[string]string{
			"role":    "assistant",
			"content": "that sounds like a great project building a reliable proxy with context compaction using anchor sitrep and tail strategy to preserve conversation history across provider switches",
		})
	}

	result := buildContext(history)

	for _, m := range result {
		if m["role"] == "system" {
			if !contains(m["content"], "[TROOPER_SITREP]") {
				t.Errorf("SITREP message missing [TROOPER_SITREP] tag, got: %s", m["content"])
			}
			if !contains(m["content"], "[/TROOPER_SITREP]") {
				t.Errorf("SITREP message missing [/TROOPER_SITREP] closing tag")
			}
			return
		}
	}
	t.Errorf("No SITREP system message found in compacted result")
}

// ── SITREP Tests ──────────────────────────────────────────────────────────────

func TestBuildSITREP_BasicExtraction(t *testing.T) {
	messages := []map[string]string{
		{"role": "user", "content": "I am building a go proxy called trooper"},
		{"role": "assistant", "content": "great project"},
	}
	sitrep := buildSITREP(messages, "building trooper proxy")

	if sitrep.Intent == "" {
		t.Errorf("buildSITREP() intent is empty")
	}
	if sitrep.Confidence == 0 {
		t.Errorf("buildSITREP() confidence is 0")
	}
}

func TestBuildSITREP_ConfidenceRange(t *testing.T) {
	messages := []map[string]string{
		{"role": "user", "content": "building trooper with ollama claude docker"},
		{"role": "assistant", "content": "resolve the issue deploy the fix"},
	}
	sitrep := buildSITREP(messages, "building trooper")

	if sitrep.Confidence < 0 || sitrep.Confidence > 1.1 {
		t.Errorf("buildSITREP() confidence out of range: %v", sitrep.Confidence)
	}
}

func TestFormatSITREP_ValidJSON(t *testing.T) {
	sitrep := SITREP{
		Intent:        "building a proxy",
		IntentSource:  "first_middle_message",
		Entities:      []string{"ollama", "claude"},
		OpenLoops:     []string{"fix the bug"},
		RecentActions: []string{"deploy monday"},
		ResolvedLoops: []string{"resolve the issue"},
		Confidence:    0.9,
	}

	result := formatSITREP(sitrep)

	if !contains(result, "[TROOPER_SITREP]") {
		t.Errorf("formatSITREP() missing opening tag")
	}
	if !contains(result, "[/TROOPER_SITREP]") {
		t.Errorf("formatSITREP() missing closing tag")
	}
	if !contains(result, "building a proxy") {
		t.Errorf("formatSITREP() missing intent")
	}
}

// ── Helper ────────────────────────────────────────────────────────────────────

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
