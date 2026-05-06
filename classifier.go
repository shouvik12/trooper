package main

import "strings"

func isSimpleTurn(message string) bool {
	msg := strings.ToLower(message)

	// 1. Code detection → always Claude
	if containsCode(msg) {
		return false
	}

	// 2. Risk patterns → always Claude
	for _, p := range riskPatterns {
		if strings.Contains(msg, p) {
			return false
		}
	}

	// 3. Length guard → long = complex
	if len(msg) > 300 || wordCount(msg) > 20 {
		return false
	}

	// 4. Safe patterns → Ollama
	for _, p := range safePatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}

	// 5. Default → Claude (safety first)
	return false
}

func containsCode(msg string) bool {
	return strings.Contains(msg, "```") ||
		strings.Contains(msg, "func ") ||
		strings.Contains(msg, "class ") ||
		strings.Contains(msg, "def ") ||
		strings.Contains(msg, "import ") ||
		strings.Contains(msg, "package ") ||
		strings.Contains(msg, "struct ") ||
		strings.Contains(msg, "=>") ||
		strings.Contains(msg, "==") ||
		strings.Contains(msg, ";")
}

func wordCount(s string) int {
	return len(strings.Fields(s))
}

var safePatterns = []string{
	// Math / factual
	"calculate", "how many", "convert", "define ", "what does",
	// Formatting
	"format this", "rewrite this as", "make this shorter",
	"make this longer", "fix the grammar", "fix spelling",
	"bullet points", "numbered list",
	// Trivial
	"spell ", "abbreviation for",
	// Conversation meta
        "what have we covered",
	"what did we discuss", "repeat that", "say that again",
	"remind me", "give me an example",
}

var riskPatterns = []string{
	// Judgment
	"should i", "should we", "would you recommend",
	"is it worth", "is it better",
	// Reasoning
	"why", "how does", "explain", "reason",
	"what causes", "what happens if",
	// Multi-step / building
	"step by step", "walk me through",
	"how do i build", "how do i implement",
	"debug", "fix this", "what's wrong",
	// Context-heavy
	"based on what we said", "given our discussion",
	"in my case", "for my use case",
        // Summary (context-dependent — default to Claude)
        "summarise", "summarize",
}
