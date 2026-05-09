package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ProviderState struct {
	mu        sync.Mutex
	FailCount int
	LastFail  time.Time
}

func main() {
	port := getEnv("TROOPER_PORT", "3000")
	bindAddr := getEnv("TROOPER_BIND", "127.0.0.1")

	chain := buildChain()
	quotaCodes := loadQuotaCodes()
	active := &ActiveProvider{index: 0}
	startHealthCheck(chain, active)
	states := map[string]*ProviderState{}
	for _, p := range chain {
		states[p.Name] = &ProviderState{}
	}

	// Warn if no cloud provider configured
	hasCloud := false
	for _, p := range chain {
		if p.Name != "ollama" {
			hasCloud = true
			break
		}
	}
	if !hasCloud {
		log.Printf("⚠️  No cloud providers configured — set at least one of: CLAUDE_API_KEY, GEMINI_API_KEY, OPENAI_API_KEY")
		log.Printf("    Trooper needs a cloud provider to fall back from.")
	}
	log.Printf("🪖  Trooper proxy starting on http://%s:%s", bindAddr, port)
	for i, p := range chain {
		log.Printf("    Provider %d: %s", i+1, p.Name)
	}
	log.Printf("    Triggers : HTTP %v", quotaCodes)
	store := NewSessionStore()
	http.HandleFunc("/", makeHandler(chain, quotaCodes, active, store, states))
	if err := http.ListenAndServe(bindAddr+":"+port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// ── Config ────────────────────────────────────────────────────────────────────

func loadQuotaCodes() map[int]bool {
	quotaCodes := map[int]bool{}
	raw := getEnv("QUOTA_STATUS_CODES", "429,402,529,400")
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if code, err := strconv.Atoi(s); err == nil {
			quotaCodes[code] = true
		}
	}
	return quotaCodes
}

// ── Handler ───────────────────────────────────────────────────────────────────

func makeHandler(chain []Provider, quotaCodes map[int]bool, active *ActiveProvider, store *SessionStore, states map[string]*ProviderState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, `{"error":"failed to read request"}`, http.StatusBadRequest)
			return
		}

		var reqMap map[string]interface{}
		json.Unmarshal(body, &reqMap)
		wantsStream, _ := reqMap["stream"].(bool)

		// Session handling
		sessionID := r.Header.Get("X-Session-ID")
		if sessionID == "" {
			sessionID = fmt.Sprintf("auto-%d", time.Now().UnixNano())
		}
		messages, err := extractMessages(body)
		if err == nil {
			store.Append(sessionID, messages)
		}

		log.Printf("📥 %s %s (stream=%v, session=%s)", r.Method, r.URL.Path, wantsStream, sessionID)

		history := store.GetTripleAnchor(sessionID)
		fallbackCount := 0
		trigger := ""

		// Extract latest user message for classification
		latestMessage := extractLatestUserMessage(body)
		simple := isSimpleTurn(latestMessage)
		if simple {
			log.Printf("🧠 Simple turn detected — routing to local")
		}

		// Try each provider
		for i := 0; i < len(chain); i++ {
			provider := chain[i]

			// Skip cloud providers if simple turn
			if simple && provider.Name != "ollama" {
				continue
			}

			// Circuit breaker — skip if provider is known to be down
			state := states[provider.Name]
			state.mu.Lock()
			shouldSkip := state.FailCount >= 3 && time.Since(state.LastFail) < 60*time.Second
			state.mu.Unlock()
			if shouldSkip {
				log.Printf("⚡ Skipping %s — circuit open (%d fails in last 60s)", provider.Name, state.FailCount)
				continue
			}
			log.Printf("🔄 Trying provider: %s", provider.Name)

			if provider.Name == "ollama" {
				saved := store.AddTokensSaved(sessionID, estimateTokens(latestMessage))
				if simple && fallbackCount == 0 {
					log.Printf("🪖 Local: ollama (simple turn) | session saved: %d tokens", saved)
					w.Header().Set("X-Trooper-Decision", "ollama (simple turn) | cloud skipped")
				} else {
					log.Printf("🪖 Fallback: %s → ollama (%s) | context preserved | session saved: %d tokens", chain[0].Name, trigger, saved)
					w.Header().Set("X-Trooper-Decision", fmt.Sprintf("ollama (fallback: %s)", trigger))
				}
				w.Header().Set("X-Trooper-Session-Saved", fmt.Sprintf("%d tokens", saved))
				w.Header().Set("X-Trooper-Provider", "ollama")
				w.Header().Set("X-Trooper-Summary", fmt.Sprintf("%s → ollama (%s) | context ✓", chain[0].Name, trigger))
				w.Header().Set("X-Trooper-Fallback-Count", fmt.Sprintf("%d", fallbackCount))
				w.Header().Set("X-Trooper-Trigger", trigger)

				if wantsStream {
					streamFallback(w, body, provider, history)
				} else {
					fallbackResp, err := callFallback(body, provider, history)
					if err != nil {
						log.Printf("❌ Ollama error: %v", err)
						http.Error(w, `{"error":"all providers failed"}`, http.StatusBadGateway)
						return
					}

					// Store assistant response in session
					var parsedResp map[string]interface{}
					if json.Unmarshal(fallbackResp, &parsedResp) == nil {
						if choices, ok := parsedResp["choices"].([]interface{}); ok && len(choices) > 0 {
							if choice, ok := choices[0].(map[string]interface{}); ok {
								if msg, ok := choice["message"].(map[string]interface{}); ok {
									if content, ok := msg["content"].(string); ok && content != "" {
										store.Append(sessionID, []map[string]string{
											{"role": "assistant", "content": content},
										})
									}
								}
							}
						}
					}
					w.Header().Set("Content-Type", "application/json")
					w.Write(fallbackResp)
				}
				return
			}

			// Try cloud provider
			resp, err := callProvider(body, r, provider)
			if err != nil {
				log.Printf("⚠️  %s network error: %v — trying next", provider.Name, err)
				fallbackCount++
				trigger = "network_error"
				states[provider.Name].mu.Lock()
				states[provider.Name].FailCount++
				states[provider.Name].LastFail = time.Now()
				states[provider.Name].mu.Unlock()
				continue
			}

			switch {
			case resp.StatusCode == http.StatusOK:
				log.Printf("✅ %s responded OK", provider.Name)
				log.Printf("🪖 Provider: %s | direct ✓", provider.Name)
				states[provider.Name].mu.Lock()
				states[provider.Name].FailCount = 0
				states[provider.Name].mu.Unlock()
				w.Header().Set("X-Trooper-Summary", fmt.Sprintf("%s (direct) ✓", provider.Name))
				w.Header().Set("X-Trooper-Provider", provider.Name)
				w.Header().Set("X-Trooper-Fallback-Count", fmt.Sprintf("%d", fallbackCount))
				w.Header().Set("X-Trooper-Trigger", trigger)
				copyResponse(w, resp)
				return

			case resp.StatusCode == http.StatusUnauthorized:
				log.Printf("❌ %s 401 — bad API key", provider.Name)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				fallbackCount++
				trigger = "401"
				states[provider.Name].mu.Lock()
				states[provider.Name].FailCount++
				states[provider.Name].LastFail = time.Now()
				states[provider.Name].mu.Unlock()
				continue

			case resp.StatusCode == 429:
				log.Printf("⚠️  %s 429 — rate limited, retrying with backoff", provider.Name)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				time.Sleep(2 * time.Second)
				resp, err = callProvider(body, r, provider)
				if err == nil && resp.StatusCode == http.StatusOK {
					log.Printf("✅ %s recovered after backoff", provider.Name)
					w.Header().Set("X-Trooper-Provider", provider.Name)
					w.Header().Set("X-Trooper-Fallback-Count", fmt.Sprintf("%d", fallbackCount))
					w.Header().Set("X-Trooper-Trigger", "429_recovered")
					copyResponse(w, resp)
					return
				}
				log.Printf("⚠️  %s still failing after backoff — trying next", provider.Name)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				fallbackCount++
				trigger = "429"
				states[provider.Name].mu.Lock()
				states[provider.Name].FailCount++
				states[provider.Name].LastFail = time.Now()
				states[provider.Name].mu.Unlock()
				continue

			case resp.StatusCode == 402:
				log.Printf("⚠️  %s 402 — credits gone, trying next", provider.Name)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				fallbackCount++
				trigger = "402"
				states[provider.Name].mu.Lock()
				states[provider.Name].FailCount++
				states[provider.Name].LastFail = time.Now()
				states[provider.Name].mu.Unlock()
				continue

			case resp.StatusCode == 400:
				bodyBytes, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if strings.Contains(string(bodyBytes), "credit balance") {
					log.Printf("⚠️  %s 400 — credit balance too low, trying next", provider.Name)
					fallbackCount++
					trigger = "credit_balance"
					states[provider.Name].mu.Lock()
					states[provider.Name].FailCount++
					states[provider.Name].LastFail = time.Now()
					states[provider.Name].mu.Unlock()
					continue
				}
				log.Printf("❌ %s 400 — bad request", provider.Name)
				w.WriteHeader(400)
				w.Write(bodyBytes)
				return

			case quotaCodes[resp.StatusCode]:
				log.Printf("⚠️  %s %d — quota hit, trying next", provider.Name, resp.StatusCode)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				fallbackCount++
				trigger = fmt.Sprintf("%d", resp.StatusCode)
				states[provider.Name].mu.Lock()
				states[provider.Name].FailCount++
				states[provider.Name].LastFail = time.Now()
				states[provider.Name].mu.Unlock()
				continue

			default:
				log.Printf("❌ %s %d — non-recoverable", provider.Name, resp.StatusCode)
				copyResponse(w, resp)
				return
			}
		}

		http.Error(w, `{"error":"all providers failed"}`, http.StatusBadGateway)
	}
}

// ── Provider Call ─────────────────────────────────────────────────────────────

func callProvider(body []byte, r *http.Request, p Provider) (*http.Response, error) {
	var reqMap map[string]interface{}
	if err := json.Unmarshal(body, &reqMap); err != nil || reqMap == nil {
		reqMap = make(map[string]interface{})
	}
	if p.Model != "" {
		if _, hasModel := reqMap["model"]; !hasModel {
			reqMap["model"] = p.Model
		}
	}
	newBody, _ := json.Marshal(reqMap)
	req, err := http.NewRequestWithContext(r.Context(), "POST", p.URL, bytes.NewBuffer(newBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	if strings.ToLower(p.AuthHeader) == "authorization" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	} else if p.AuthHeader != "" {
		req.Header.Set(p.AuthHeader, p.APIKey)
	}

	if p.Name == "claude" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	return client.Do(req)
}

// ── Fallback (Ollama) ─────────────────────────────────────────────────────────

func callFallback(body []byte, p Provider, history []map[string]string) ([]byte, error) {
	messages := buildContext(history)
	if len(messages) == 0 {
		var err error
		messages, err = extractMessages(body)
		if err != nil {
			return nil, fmt.Errorf("extracting messages: %w", err)
		}
	}

	ollamaReq := map[string]interface{}{
		"model":    p.Model,
		"messages": messages,
		"stream":   false,
	}
	reqBytes, _ := json.Marshal(ollamaReq)

	resp, err := http.Post(p.URL, "application/json", bytes.NewBuffer(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("ollama unreachable at %s: %w", p.URL, err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)

	var parsed map[string]interface{}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, fmt.Errorf("ollama response parse error: %w", err)
	}

	text := extractFallbackText(parsed)
	return wrapAsOpenAI(text, p.Model), nil
}

func streamFallback(w http.ResponseWriter, body []byte, p Provider, history []map[string]string) {
	messages := buildContext(history)
	if len(messages) == 0 {
		var err error
		messages, err = extractMessages(body)
		if err != nil {
			http.Error(w, `{"error":"failed to parse messages"}`, http.StatusBadRequest)
			return
		}
	}

	ollamaReq := map[string]interface{}{
		"model":    p.Model,
		"messages": messages,
		"stream":   true,
	}
	reqBytes, _ := json.Marshal(ollamaReq)

	resp, err := http.Post(p.URL, "application/json", bytes.NewBuffer(reqBytes))
	if err != nil {
		http.Error(w, `{"error":"ollama unreachable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Trooper-Fallback", p.Model)

	flusher, canFlush := w.(http.Flusher)
	decoder := json.NewDecoder(resp.Body)

	fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"role\":\"assistant\"}}\n\n")
	if canFlush {
		flusher.Flush()
	}

	for {
		var chunk map[string]interface{}
		if err := decoder.Decode(&chunk); err != nil {
			break
		}

		text := extractFallbackText(chunk)
		if text != "" {
			delta := map[string]interface{}{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]string{"type": "text_delta", "text": text},
			}
			deltaBytes, _ := json.Marshal(delta)
			fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", deltaBytes)
			if canFlush {
				flusher.Flush()
			}
		}

		if done, _ := chunk["done"].(bool); done {
			break
		}
	}

	fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	if canFlush {
		flusher.Flush()
	}
}

// ── Normalization ─────────────────────────────────────────────────────────────

func normalize(text string) string {
	text = strings.ToLower(text)

	replacements := map[string]string{
		"fails":   "fail",
		"failed":  "fail",
		"failing": "fail",

		"fixed":     "resolve",
		"resolved":  "resolve",
		"resolving": "resolve",

		"errors":   "error",
		"issues":   "issue",
		"problems": "problem",
	}

	for k, v := range replacements {
		text = strings.ReplaceAll(text, k, v)
	}

	return text
}

// ── Signal Classification ─────────────────────────────────────────────────────

type SignalType string

const (
	SignalOpenLoop SignalType = "open_loop"
	SignalAction   SignalType = "action"
	SignalResolved SignalType = "resolved"
)

var openLoopWords = []string{
	"broken", "pending", "fail", "issue", "problem",
	"stuck", "blocked", "unclear", "missing", "wrong",
}

var resolvedLoopWords = []string{
	"resolve", "done", "confirmed", "locked", "completed",
	"working", "closed", "shipped", "merged",
}

var actionWords = []string{
	"restart", "deploy", "check", "update", "switch",
	"migrate", "rollback", "enable", "disable", "configure",
}

func classifyWord(word string) SignalType {
	for _, o := range openLoopWords {
		if strings.HasPrefix(word, o) {
			return SignalOpenLoop
		}
	}
	for _, r := range resolvedLoopWords {
		if strings.HasPrefix(word, r) {
			return SignalResolved
		}
	}
	for _, a := range actionWords {
		if strings.HasPrefix(word, a) {
			return SignalAction
		}
	}
	return ""
}

// ── Phrase Extraction ─────────────────────────────────────────────────────────

func extractForwardPhrase(words []string, i int) string {
	end := i + 5
	if end > len(words) {
		end = len(words)
	}

	phraseWords := []string{}
	for j := i; j < end; j++ {
		w := strings.Trim(words[j], ".,!?:;\"'()")
		if w == "" {
			continue
		}
		phraseWords = append(phraseWords, w)

		if strings.HasSuffix(words[j], ".") || strings.HasSuffix(words[j], ",") {
			break
		}
	}

	return strings.Join(phraseWords, " ")
}

// ── SITREP Extraction ─────────────────────────────────────────────────────────

var intentVerbs = []string{
	"building", "creating", "fixing", "designing",
	"implementing", "adding", "removing", "updating",
	"trying", "want", "need", "working on",
	"debugging", "testing", "deploying", "migrating",
}

var tier1Entities = []string{
	"redis", "ollama", "claude", "trooper", "gemini", "openai",
	"docker", "postgres", "mysql", "kafka", "nginx", "kubernetes",
}

var tier3Entities = []string{
	"proxy", "server", "session", "fallback", "token",
	"cache", "queue", "handler", "router", "middleware",
}

type SITREP struct {
	Intent        string
	IntentSource  string
	Entities      []string
	OpenLoops     []string
	RecentActions []string
	ResolvedLoops []string
	Confidence    float64
}

func extractIntent(messages []map[string]string, latestUser string) (string, string, float64) {
	if len(messages) > 0 {
		content := strings.ToLower(messages[0]["content"])
		for _, verb := range intentVerbs {
			if strings.Contains(content, verb) {
				idx := strings.Index(content, verb)
				end := idx + 60
				if end > len(content) {
					end = len(content)
				}
				return strings.TrimSpace(content[idx:end]), "first_middle_message", 0.4
			}
		}
	}

	if latestUser != "" {
		content := strings.ToLower(latestUser)
		for _, verb := range intentVerbs {
			if strings.Contains(content, verb) {
				idx := strings.Index(content, verb)
				end := idx + 60
				if end > len(content) {
					end = len(content)
				}
				return strings.TrimSpace(content[idx:end]), "latest_user_message", 0.3
			}
		}
	}

	freq := map[string]int{}
	for _, m := range messages {
		words := strings.Fields(strings.ToLower(m["content"]))
		for _, w := range words {
			w = strings.Trim(w, ".,!?:;\"'")
			if len(w) > 4 {
				freq[w]++
			}
		}
	}
	topWord := ""
	topCount := 0
	for w, c := range freq {
		if c > topCount {
			topWord = w
			topCount = c
		}
	}
	if topWord != "" {
		return topWord, "keyword_frequency", 0.2
	}

	return "Unknown", "none", 0.0
}

func extractEntities(messages []map[string]string) []string {
	seen := map[string]bool{}
	tier1 := []string{}
	tier2 := []string{}
	tier3 := []string{}

	for _, m := range messages {
		words := strings.Fields(m["content"])
		for _, w := range words {
			clean := strings.Trim(w, ".,!?:;\"'()")
			lower := strings.ToLower(clean)

			if seen[lower] || clean == "" {
				continue
			}

			for _, t1 := range tier1Entities {
				if lower == t1 {
					tier1 = append(tier1, clean)
					seen[lower] = true
				}
			}
			if seen[lower] {
				continue
			}

			if strings.HasSuffix(lower, ".go") ||
				strings.HasSuffix(lower, ".yaml") ||
				strings.HasSuffix(lower, ".yml") ||
				strings.HasSuffix(lower, ".json") {
				tier1 = append(tier1, clean)
				seen[lower] = true
				continue
			}

			if clean == strings.ToUpper(clean) && strings.Contains(clean, "_") && len(clean) > 3 {
				tier1 = append(tier1, clean)
				seen[lower] = true
				continue
			}

			if code, err := strconv.Atoi(clean); err == nil {
				if code == 400 || code == 401 || code == 429 || code == 402 || code == 529 {
					tier2 = append(tier2, clean)
					seen[lower] = true
					continue
				}
			}

			if strings.HasSuffix(lower, "k") || strings.HasSuffix(lower, "hr") || strings.HasSuffix(lower, "mb") {
				tier2 = append(tier2, clean)
				seen[lower] = true
				continue
			}

			for _, t3 := range tier3Entities {
				if lower == t3 {
					tier3 = append(tier3, clean)
					seen[lower] = true
				}
			}
		}
	}

	result := []string{}
	result = append(result, tier1...)
	result = append(result, tier2...)
	result = append(result, tier3...)
	if len(result) > 5 {
		result = result[:5]
	}
	return result
}

func extractSignals(messages []map[string]string) (openLoops, recentActions, resolvedLoops []string) {
	seenOpen := map[string]bool{}
	seenActions := map[string]bool{}
	seenResolved := map[string]bool{}

	startIdx := 0
	if len(messages) > 6 {
		startIdx = len(messages) - 6
	}

	for _, m := range messages[startIdx:] {
		content := normalize(m["content"])
		words := strings.Fields(content)

		for i, raw := range words {
			word := strings.Trim(raw, ".,!?:;\"'()")
			if word == "" {
				continue
			}

			signalType := classifyWord(word)
			if signalType == "" {
				continue
			}

			phrase := extractForwardPhrase(words, i)
			if len(phrase) < 4 {
				continue
			}

			switch signalType {
			case SignalOpenLoop:
				if !seenOpen[phrase] {
					openLoops = append(openLoops, phrase)
					seenOpen[phrase] = true
				}
			case SignalResolved:
				if !seenResolved[phrase] {
					resolvedLoops = append(resolvedLoops, phrase)
					seenResolved[phrase] = true
				}
			case SignalAction:
				if !seenActions[phrase] {
					recentActions = append(recentActions, phrase)
					seenActions[phrase] = true
				}
			}
		}
	}

	if len(openLoops) > 5 {
		openLoops = openLoops[:5]
	}
	if len(recentActions) > 5 {
		recentActions = recentActions[:5]
	}
	if len(resolvedLoops) > 5 {
		resolvedLoops = resolvedLoops[:5]
	}
	return
}

func extractConstraints(entities []string) []string {
	constraints := []string{}
	knownConstraints := map[string]string{
		"ollama":    "local-first",
		"trooper":   "proxy-layer",
		"openai":    "openai-compatible",
		"claude":    "anthropic-compatible",
		"gemini":    "gemini-compatible",
		"docker":    "containerized",
		"streaming": "streaming-required",
	}
	seen := map[string]bool{}
	for _, e := range entities {
		lower := strings.ToLower(e)
		if c, ok := knownConstraints[lower]; ok {
			if !seen[c] {
				constraints = append(constraints, c)
				seen[c] = true
			}
		}
	}
	if len(constraints) == 0 {
		constraints = append(constraints, "general")
	}
	return constraints
}

func buildSITREP(middleMessages []map[string]string, latestUser string) SITREP {
	intent, source, intentScore := extractIntent(middleMessages, latestUser)
	entities := extractEntities(middleMessages)
	openLoops, recentActions, resolvedLoops := extractSignals(middleMessages)

	confidence := intentScore
	if len(entities) >= 3 {
		confidence += 0.3
	} else if len(entities) > 0 {
		confidence += 0.1
	}
	total := len(openLoops) + len(recentActions) + len(resolvedLoops)
	if total >= 2 {
		confidence += 0.3
	} else if total > 0 {
		confidence += 0.1
	}

	return SITREP{
		Intent:        intent,
		IntentSource:  source,
		Entities:      entities,
		OpenLoops:     openLoops,
		RecentActions: recentActions,
		ResolvedLoops: resolvedLoops,
		Confidence:    confidence,
	}
}

func intentStage(source string) string {
	switch source {
	case "first_middle_message":
		return "in_progress"
	case "latest_user_message":
		return "debugging"
	case "keyword_frequency":
		return "unclear"
	default:
		return "unknown"
	}
}

func formatSITREP(s SITREP) string {
	type sitrepJSON struct {
		Intent         string   `json:"intent"`
		Stage          string   `json:"stage"`
		Constraints    []string `json:"constraints"`
		ActiveEntities []string `json:"active_entities"`
		OpenLoops      []string `json:"open_loops"`
		RecentActions  []string `json:"recent_actions"`
		ResolvedLoops  []string `json:"resolved_loops"`
		Confidence     float64  `json:"confidence"`
	}

	payload := sitrepJSON{
		Intent:         s.Intent,
		Stage:          intentStage(s.IntentSource),
		Constraints:    extractConstraints(s.Entities),
		ActiveEntities: s.Entities,
		OpenLoops:      s.OpenLoops,
		RecentActions:  s.RecentActions,
		ResolvedLoops:  s.ResolvedLoops,
		Confidence:     s.Confidence,
	}

	out, _ := json.Marshal(payload)
	return "[TROOPER_SITREP]" + string(out) + "[/TROOPER_SITREP]"
}

// ── Context Compaction ────────────────────────────────────────────────────────

func estimateTokens(s string) int {
	return len(s) / 4
}

func buildContext(history []map[string]string) []map[string]string {
	contextWindow := 6144
	if v := getEnv("CONTEXT_WINDOW", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			contextWindow = n
		}
	}

	recentBudget := contextWindow * 70 / 100

	if len(history) == 0 {
		return history
	}

	totalTokens := 0
	for _, m := range history {
		totalTokens += estimateTokens(m["content"])
	}

	if totalTokens <= contextWindow {
		log.Printf("📦  No compaction needed — %d tokens fits within %d budget", totalTokens, contextWindow)
		return history
	}

	log.Printf("📦  Context compaction triggered — %d tokens exceeds %d budget", totalTokens, contextWindow)

	anchorMessages := []map[string]string{}
	anchorTokens := 0
	anchorEnd := 0
	for i, m := range history {
		if i >= 2 {
			anchorEnd = i
			break
		}
		anchorMessages = append(anchorMessages, m)
		anchorTokens += estimateTokens(m["content"])
	}
	if anchorEnd == 0 {
		anchorEnd = len(anchorMessages)
	}

	recentMessages := []map[string]string{}
	recentTokens := 0
	recentStart := len(history)
	for i := len(history) - 1; i >= anchorEnd; i-- {
		t := estimateTokens(history[i]["content"])
		if recentTokens+t > recentBudget {
			recentStart = i + 1
			break
		}
		recentMessages = append([]map[string]string{history[i]}, recentMessages...)
		recentTokens += t
		recentStart = i
	}

	middleMessages := history[anchorEnd:recentStart]

	latestUser := ""
	for i := len(history) - 1; i >= 0; i-- {
		if history[i]["role"] == "user" {
			latestUser = history[i]["content"]
			break
		}
	}

	sitrep := buildSITREP(middleMessages, latestUser)
	sitrepText := formatSITREP(sitrep)

	result := []map[string]string{}
	result = append(result, anchorMessages...)
	result = append(result, map[string]string{
		"role":    "system",
		"content": sitrepText,
	})
	result = append(result, recentMessages...)

	totalUsed := anchorTokens + estimateTokens(sitrepText) + recentTokens
	log.Printf("📦  Context compaction complete")
	log.Printf("    Total turns    : %d", len(history))
	log.Printf("    Anchor turns   : %d (~%d tokens)", len(anchorMessages), anchorTokens)
	log.Printf("    Middle turns   : %d → SITREP (~%d tokens)", len(middleMessages), estimateTokens(sitrepText))
	log.Printf("    Recent turns   : %d (~%d tokens)", len(recentMessages), recentTokens)
	log.Printf("    Tokens used    : %d / %d", totalUsed, contextWindow)
	log.Printf("    SITREP         : intent=%q stage=%s confidence=%.2f open=%d actions=%d resolved=%d",
		sitrep.Intent, intentStage(sitrep.IntentSource), sitrep.Confidence,
		len(sitrep.OpenLoops), len(sitrep.RecentActions), len(sitrep.ResolvedLoops))

	return result
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func extractFallbackText(parsed map[string]interface{}) string {
	if msg, ok := parsed["message"].(map[string]interface{}); ok {
		if text, ok := msg["content"].(string); ok {
			return text
		}
	}
	if text, ok := parsed["response"].(string); ok {
		return text
	}
	return ""
}

func extractLatestUserMessage(body []byte) string {
	messages, err := extractMessages(body)
	if err != nil || len(messages) == 0 {
		return ""
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i]["role"] == "user" {
			return messages[i]["content"]
		}
	}
	return ""
}

func extractMessages(body []byte) ([]map[string]string, error) {
	var reqMap map[string]interface{}
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return nil, err
	}

	raw, ok := reqMap["messages"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("missing or invalid messages field")
	}

	var messages []map[string]string
	for _, m := range raw {
		msg, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		text := extractContent(msg["content"])
		messages = append(messages, map[string]string{
			"role":    role,
			"content": text,
		})
	}
	return messages, nil
}

func extractContent(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, block := range v {
			b, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			if b["type"] == "text" {
				if t, ok := b["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func wrapAsOpenAI(text string, model string) []byte {
	resp := map[string]interface{}{
		"id":     "trooper-fallback",
		"object": "chat.completion",
		"model":  model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": text},
				"finish_reason": "stop",
			},
		},
		"type": "message",
		"role": "assistant",
		"content": []map[string]string{
			{"type": "text", "text": text},
		},
		"stop_reason": "end_turn",
		"usage": map[string]int{
			"input_tokens":      0,
			"output_tokens":     0,
			"prompt_tokens":     0,
			"completion_tokens": 0,
		},
	}
	out, _ := json.Marshal(resp)
	return out
}

func copyResponse(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
