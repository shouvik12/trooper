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
	http.HandleFunc("/recovery/", recoveryHandler(store))
	http.HandleFunc("/dashboard", dashboardIndexHandler(store))
	http.HandleFunc("/sessions", sessionsHandler(store))
	http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
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
		if r.Method == http.MethodHead || (r.Method == http.MethodGet && r.URL.Path == "/") {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, `{"error":"failed to read request"}`, http.StatusBadRequest)
			return
		}

		var reqMap map[string]interface{}
		json.Unmarshal(body, &reqMap)
		wantsStream, _ := reqMap["stream"].(bool)
		forceLocal, _ := reqMap["x_force_local"].(bool)

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
		currentProvider := ""
		if len(chain) > 0 {
			currentProvider = chain[0].Name
		}

		// Extract latest user message for classification
		latestMessage := extractLatestUserMessage(body)
		simple := isSimpleTurn(latestMessage)
		if simple {
			log.Printf("🧠 Simple turn detected — routing to local")
		}
		if forceLocal {
			log.Printf("🔒 Developer requested local-only (x_force_local) — skipping cloud")
		}

		// Try each provider
		for i := 0; i < len(chain); i++ {
			provider := chain[i]

			// Skip cloud providers if simple turn
			if (simple || forceLocal) && provider.Name != "ollama" {
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

				// Record fallback event if we switched providers
				if fallbackCount > 0 && currentProvider != "ollama" {
					reason := trigger
					if reason == "" {
						reason = "unknown"
					}
					store.AddFallbackEvent(sessionID, FallbackEvent{
						FromProvider: currentProvider,
						ToProvider:   "ollama",
						Reason:       reason,
						At:           time.Now(),
					})
				}

				if forceLocal && fallbackCount == 0 {
					log.Printf("🔒 Local: ollama (force_local) | privacy mode | session saved: %d tokens", saved)
					w.Header().Set("X-Trooper-Decision", "ollama (force_local) | privacy mode | complex turn kept local")
				} else if simple && fallbackCount == 0 {
					log.Printf("🧠 Local: ollama (simple turn) | session saved: %d tokens", saved)
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
										store.AppendWithMeta(sessionID, []MessageEntry{
											{Role: "assistant", Content: content, Provider: "ollama", At: time.Now()},
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
			cloudBody := body
			if store.HasSITREP(sessionID) {
				if compressed := store.BuildCompressedBody(sessionID, body); compressed != nil {
					cloudBody = compressed
					log.Printf("🗜️  History compressed for %s", provider.Name)
				}
			}
			resp, err := callProvider(cloudBody, r, provider)
			if err != nil {
				log.Printf("⚠️  %s network error: %v — trying next", provider.Name, err)
				if fallbackCount == 0 {
					currentProvider = provider.Name
				}
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
				copyAndStoreResponse(w, resp, store, sessionID, provider.Name)
				return

			case resp.StatusCode == http.StatusUnauthorized:
				log.Printf("❌ %s 401 — bad API key", provider.Name)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if fallbackCount == 0 {
					currentProvider = provider.Name
				}
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
				if fallbackCount == 0 {
					currentProvider = provider.Name
				}
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
				if fallbackCount == 0 {
					currentProvider = provider.Name
				}
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
				if strings.Contains(string(bodyBytes), "credit balance") || strings.Contains(string(bodyBytes), "invalid") || strings.Contains(string(bodyBytes), "api_key") || strings.Contains(string(bodyBytes), "authentication") {
					log.Printf("⚠️  %s 400 — credit balance too low, trying next", provider.Name)
					if fallbackCount == 0 {
						currentProvider = provider.Name
					}
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
				if fallbackCount == 0 {
					currentProvider = provider.Name
				}
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
	end := i + 8
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
				end := idx + 100
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
				end := idx + 100
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
			if len(strings.Fields(phrase)) < 2 {
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

// ── Recovery Endpoint ─────────────────────────────────────────────────────────

func recoveryHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || (r.Method == http.MethodGet && r.URL.Path == "/") {
			w.WriteHeader(http.StatusOK)
			return
		}
		sessionID := strings.TrimPrefix(r.URL.Path, "/recovery/")
		if sessionID == "" {
			http.Error(w, `{"error":"session_id required"}`, http.StatusBadRequest)
			return
		}

		messages := store.GetAll(sessionID)
		if messages == nil {
			http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
			return
		}

		completedSteps := extractCompletedSteps(messages)
		resumeFrom := len(completedSteps) + 1

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"session_id":      sessionID,
			"completed_steps": completedSteps,
			"resume_from":     resumeFrom,
			"recovery_hint":   fmt.Sprintf("Resume from step %d", resumeFrom),
		})
	}
}

// ── Copy and Store Response ───────────────────────────────────────────────────

func copyAndStoreResponse(w http.ResponseWriter, resp *http.Response, store *SessionStore, sessionID string, providerName string) {
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	// Extract and store assistant message
	var parsed map[string]interface{}
	if json.Unmarshal(bodyBytes, &parsed) == nil {
		text := ""
		// Claude native format
		if content, ok := parsed["content"].([]interface{}); ok && len(content) > 0 {
			if block, ok := content[0].(map[string]interface{}); ok {
				if t, ok := block["text"].(string); ok {
					text = t
				}
			}
		}
		// OpenAI format
		if text == "" {
			if choices, ok := parsed["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if msg, ok := choice["message"].(map[string]interface{}); ok {
						if t, ok := msg["content"].(string); ok {
							text = t
						}
					}
				}
			}
		}
		if text != "" {
			store.AppendWithMeta(sessionID, []MessageEntry{
				{Role: "assistant", Content: text, Provider: providerName, At: time.Now()},
			})
		}
	}

	w.Write(bodyBytes)
}

// ── Dashboard Handler ─────────────────────────────────────────────────────────

func dashboardHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("session")
		if sessionID == "" {
			http.Error(w, "session parameter required", http.StatusBadRequest)
			return
		}

		messages := store.GetAll(sessionID)
		entries := store.GetEntries(sessionID)
		fallbackEvents := store.GetFallbackEvents(sessionID)
		completed := extractCompletedSteps(messages)
		sitrep := buildSITREP(messages, extractLatestUserMessage2(messages))
		tokensSaved := store.GetTokensSaved(sessionID)
		compressionRatio := store.GetCompressionRatio(sessionID)
		rawSITREP := store.GetRawSITREP(sessionID)
		startTime := store.GetStartTime(sessionID)
		duration := ""
		if !startTime.IsZero() {
			d := time.Since(startTime)
			duration = fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
		}

		// Determine current provider (last known)
		currentProvider := "unknown"
		if len(entries) > 0 {
			for i := len(entries) - 1; i >= 0; i-- {
				if entries[i].Provider != "" {
					currentProvider = entries[i].Provider
					break
				}
			}
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, dashboardHTML,
			sessionID,
			sessionID,
			sitrep.Intent,
			sitrep.Intent,
			currentProvider,
			tokensSaved,
			compressionRatio,
			fmt.Sprintf("%.0f%%", sitrep.Confidence*100),
			duration,
			len(fallbackEvents),
			renderEntities(sitrep.Entities),
			len(fallbackEvents),
			renderFallbackEvents(fallbackEvents),
			len(completed),
			renderCompleted(completed),
			len(sitrep.OpenLoops),
			renderOpenLoopsNew(sitrep.OpenLoops),
			len(sitrep.ResolvedLoops),
			renderResolvedLoops(sitrep.ResolvedLoops),
			rawSITREP,
			renderTranscript(entries),
		)
	}
}

// Replace the existing dashboardHTML variable in main.go with this one.

var dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Trooper — %s</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:monospace;background:linear-gradient(135deg,#0f1117 0%%,#131928 50%%,#0d1117 100%%);min-height:100vh;color:#c9d1d9;font-size:13px;position:relative}
body::before{content:'';position:fixed;top:0;left:0;right:0;bottom:0;background:radial-gradient(ellipse at 20%% 10%%,rgba(56,139,255,0.08) 0%%,transparent 50%%),radial-gradient(ellipse at 80%% 80%%,rgba(63,185,80,0.05) 0%%,transparent 50%%);pointer-events:none;z-index:0}
.wrap{position:relative;z-index:1;max-width:1200px;margin:0 auto}
.topbar{backdrop-filter:blur(20px);background:rgba(22,27,34,0.8);border-bottom:1px solid rgba(48,54,61,0.6);padding:12px 20px;display:flex;align-items:center;gap:12px}
.logo{font-size:15px;font-weight:700;color:#58a6ff;letter-spacing:-0.3px}
.live-badge{display:flex;align-items:center;gap:5px;background:rgba(63,185,80,0.12);border:1px solid rgba(63,185,80,0.25);border-radius:20px;padding:2px 10px;font-size:11px;color:#3fb950}
.dot{width:5px;height:5px;border-radius:50%%;background:#3fb950;animation:pulse 1.8s ease-in-out infinite}
@keyframes pulse{0%%,100%%{opacity:1;transform:scale(1)}50%%{opacity:0.4;transform:scale(0.8)}}
.session-id{font-size:11px;color:#6e7681;margin-left:auto}
.metrics-strip{backdrop-filter:blur(12px);background:rgba(22,27,34,0.6);border-bottom:1px solid rgba(48,54,61,0.4);padding:0 20px;display:flex;align-items:stretch;flex-wrap:wrap}
.metric{display:flex;flex-direction:column;justify-content:center;gap:3px;padding:10px 24px 10px 0;border-right:1px solid rgba(48,54,61,0.4);margin-right:24px}
.metric:last-child{border-right:none;margin-right:0}
.mlabel{font-size:9px;color:#6e7681;text-transform:uppercase;letter-spacing:1px}
.mval{font-size:13px;font-weight:600;color:#e6edf3;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:260px}
.mval.intent{color:#79c0ff;max-width:800px}
.mval.green{color:#3fb950}
.mval.blue{color:#58a6ff}
.mval.orange{color:#f0883e}
.entities{padding:8px 20px;background:rgba(13,17,23,0.4);border-bottom:1px solid rgba(33,38,45,0.6);display:flex;align-items:center;gap:6px;flex-wrap:wrap}
.elabel{font-size:9px;color:#6e7681;text-transform:uppercase;letter-spacing:1px;margin-right:2px}
.tag{font-size:10px;padding:2px 8px;border-radius:20px;background:rgba(56,139,255,0.1);color:#79c0ff;border:1px solid rgba(56,139,255,0.2)}
.grid{display:grid;grid-template-columns:1fr 1fr}
.full{grid-column:1/-1}
.panel{backdrop-filter:blur(8px);background:rgba(22,27,34,0.5);border-bottom:1px solid rgba(33,38,45,0.5);border-right:1px solid rgba(33,38,45,0.5);padding:14px 18px}
.panel.no-right{border-right:none}
.panel.no-bottom{border-bottom:none}
.ph{display:flex;align-items:center;gap:8px;margin-bottom:11px}
.ph-icon{width:22px;height:22px;border-radius:6px;display:flex;align-items:center;justify-content:center;font-size:11px;flex-shrink:0}
.ph-icon.orange{background:rgba(240,136,62,0.15);border:1px solid rgba(240,136,62,0.2)}
.ph-icon.yellow{background:rgba(210,153,34,0.15);border:1px solid rgba(210,153,34,0.2)}
.ph-icon.green{background:rgba(63,185,80,0.15);border:1px solid rgba(63,185,80,0.2)}
.ph-icon.blue{background:rgba(88,166,255,0.15);border:1px solid rgba(88,166,255,0.2)}
.ph-icon.purple{background:rgba(139,92,246,0.15);border:1px solid rgba(139,92,246,0.2)}
.pt{font-size:10px;font-weight:600;text-transform:uppercase;letter-spacing:1px;color:#8b949e}
.pc{margin-left:auto;font-size:10px;background:rgba(33,38,45,0.8);color:#6e7681;padding:1px 7px;border-radius:10px;border:1px solid rgba(48,54,61,0.4)}
.fb{background:rgba(240,136,62,0.06);border:1px solid rgba(240,136,62,0.18);border-radius:8px;padding:10px 12px;margin-bottom:6px;display:flex;gap:10px;align-items:flex-start}
.fb-icon{font-size:13px;flex-shrink:0;margin-top:1px}
.fb-title{font-size:12px;color:#f0883e;font-weight:600;display:flex;align-items:center;gap:6px;margin-bottom:4px}
.fb-detail{font-size:11px;color:#8b949e;line-height:1.5}
.pill{display:inline-block;font-size:10px;padding:1px 8px;border-radius:10px;font-weight:600}
.pill.from{background:rgba(33,38,45,0.8);color:#8b949e;border:1px solid rgba(48,54,61,0.5)}
.pill.to{background:rgba(88,166,255,0.12);color:#79c0ff;border:1px solid rgba(88,166,255,0.25)}
.fb-time{font-size:10px;color:#6e7681;margin-left:auto}
.loop{display:flex;align-items:flex-start;gap:8px;padding:7px 10px;border-radius:7px;margin-bottom:5px;font-size:12px;line-height:1.4}
.loop.open{background:rgba(210,153,34,0.07);border:1px solid rgba(210,153,34,0.15)}
.loop.resolved{background:rgba(63,185,80,0.05);border:1px solid rgba(63,185,80,0.12)}
.loop-dot{width:5px;height:5px;border-radius:50%%;margin-top:4px;flex-shrink:0}
.loop-dot.open{background:#d29922}
.loop-dot.resolved{background:#3fb950}
.loop-text.open{color:#e3b341}
.loop-text.resolved{color:#56d364;text-decoration:line-through;opacity:0.7}
.step{display:flex;align-items:flex-start;gap:8px;padding:6px 0;border-bottom:1px solid rgba(33,38,45,0.5);font-size:12px}
.step:last-child{border-bottom:none}
.snum{font-size:10px;color:#6e7681;min-width:18px;margin-top:1px}
.scheck{margin-left:auto;color:#3fb950;font-size:11px;flex-shrink:0;background:rgba(63,185,80,0.1);border:1px solid rgba(63,185,80,0.2);border-radius:4px;padding:0 4px}
.transcript{display:flex;flex-direction:column;gap:7px}
.turn{padding:9px 12px;border-radius:8px}
.turn.user{background:rgba(22,27,34,0.7);border:1px solid rgba(48,54,61,0.5)}
.turn.assistant{background:rgba(56,139,255,0.04);border:1px solid rgba(56,139,255,0.12)}
.turn.fallback-event{background:rgba(240,136,62,0.05);border:1px solid rgba(240,136,62,0.2)}
.turn-header{display:flex;align-items:center;gap:8px;margin-bottom:5px}
.trole{font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:0.8px}
.trole.user{color:#6e7681}
.trole.assistant{color:#79c0ff}
.trole.fallback{color:#f0883e}
.tprovider{font-size:10px;background:rgba(33,38,45,0.8);color:#6e7681;padding:1px 7px;border-radius:10px;border:1px solid rgba(48,54,61,0.4)}
.tprovider.new{background:rgba(56,139,255,0.1);color:#79c0ff;border:1px solid rgba(56,139,255,0.25)}
.ttime{font-size:10px;color:#6e7681;margin-left:auto}
.turn-text{font-size:12px;color:#c9d1d9;line-height:1.5;white-space:pre-wrap;word-break:break-word}
.turn-text.fb{color:#f0883e;font-size:11px}
.empty{color:#484f58;font-size:12px;padding:4px 0}
.footer{color:#484f58;font-size:11px;padding:10px 20px;border-top:1px solid rgba(33,38,45,0.5);backdrop-filter:blur(8px);background:rgba(13,17,23,0.4)}
</style>
</head>
<body>
<div class="wrap">
<div class="topbar">
  <span class="logo">&#x1FA96; Trooper</span>
  <span class="live-badge"><span class="dot"></span>live</span>
  <span class="session-id">%s</span>
</div>

<div class="metrics-strip">
  <div class="metric">
    <span class="mlabel">Intent</span>
    <span class="mval intent" title="%s">%s</span>
  </div>
  <div class="metric">
    <span class="mlabel">Provider</span>
    <span class="mval blue">%s</span>
  </div>
  <div class="metric">
    <span class="mlabel">Tokens saved</span>
    <span class="mval">%d</span>
  </div>
  <div class="metric">
    <span class="mlabel">History compressed</span>
    <span class="mval green">%s</span>
  </div>
  <div class="metric">
    <span class="mlabel">Confidence</span>
    <span class="mval green">%s</span>
  </div>
  <div class="metric">
    <span class="mlabel">Duration</span>
    <span class="mval">%s</span>
  </div>
  <div class="metric">
    <span class="mlabel">Fallbacks</span>
    <span class="mval orange">%d</span>
  </div>
</div>

<div class="entities">
  <span class="elabel">Entities</span>
  %s
</div>

<div class="grid">
  <div class="panel">
    <div class="ph"><div class="ph-icon orange">&#x26A1;</div><span class="pt">Fallback events</span><span class="pc">%d</span></div>
    %s
  </div>

  <div class="panel no-right">
    <div class="ph"><div class="ph-icon blue">&#x1F4CB;</div><span class="pt">Completed steps</span><span class="pc">%d</span></div>
    %s
  </div>

  <div class="panel">
    <div class="ph"><div class="ph-icon yellow">&#x1F513;</div><span class="pt">Open loops</span><span class="pc">%d</span></div>
    %s
  </div>

  <div class="panel no-right">
    <div class="ph"><div class="ph-icon green">&#x2705;</div><span class="pt">Resolved loops</span><span class="pc">%d</span></div>
    %s
  </div>

  <div class="panel full no-right">
    <div class="ph"><div class="ph-icon green">&#x1F4E1;</div><span class="pt">Session SITREP</span></div>
    <pre style="font-size:11px;color:#79c0ff;white-space:pre-wrap;line-height:1.6;margin:0">%s</pre>
  </div>

  <div class="panel full no-right no-bottom">
    <div class="ph"><div class="ph-icon purple">&#x1F4DC;</div><span class="pt">Session transcript</span></div>
    <div class="transcript">%s</div>
  </div>
</div>

<div class="footer">Auto-refreshes every 5s &nbsp;&middot;&nbsp; zero instrumentation &nbsp;&middot;&nbsp; local &amp; private</div>
</div>
<script>setTimeout(()=>location.reload(),5000)</script>
</body>
</html>`

// ── Dashboard Index Handler ───────────────────────────────────────────────────

func dashboardIndexHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("session") != "" {
			dashboardHandler(store)(w, r)
			return
		}

		store.mu.RLock()
		sessions := make([]string, 0, len(store.sessions))
		for id := range store.sessions {
			sessions = append(sessions, id)
		}
		store.mu.RUnlock()

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Trooper — Sessions</title>
<style>
body{font-family:monospace;max-width:900px;margin:40px auto;padding:20px;background:#0d1117;color:#e0e0e0}
h1{color:#4ade80}
.session{border:1px solid #333;border-radius:8px;padding:16px;margin:12px 0;background:#161b22;cursor:pointer}
.session:hover{border-color:#4ade80}
.session a{color:#4ade80;text-decoration:none;font-size:16px}
.empty{color:#555;margin-top:40px}
</style>
</head>
<body>
<h1>&#x1FA96; Trooper</h1>
<p style="color:#888">Active sessions</p>
`)
		if len(sessions) == 0 {
			fmt.Fprintf(w, `<p class="empty">No active sessions. Point your agent at Trooper to get started.</p>`)
		} else {
			for _, id := range sessions {
				fmt.Fprintf(w, `<div class="session"><a href="/dashboard?session=%s">%s →</a></div>`, id, id)
			}
		}
		fmt.Fprintf(w, `<p style="color:#555;font-size:12px;margin-top:40px">Auto-refreshes every 5 seconds</p>
<script>setTimeout(()=>location.reload(),5000)</script>
</body></html>`)
	}
}

// ── Sessions Handler ──────────────────────────────────────────────────────────

func sessionsHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store.mu.RLock()
		sessions := make([]string, 0, len(store.sessions))
		for id := range store.sessions {
			sessions = append(sessions, id)
		}
		store.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"sessions": sessions,
			"count":    len(sessions),
		})
	}
}

// ── Render Helpers ────────────────────────────────────────────────────────────

func extractLatestUserMessage2(messages []map[string]string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i]["role"] == "user" {
			return messages[i]["content"]
		}
	}
	return ""
}

func renderEntities(items []string) string {
	if len(items) == 0 {
		return `<span style="color:#484f58;font-size:12px">none detected</span>`
	}
	out := ""
	for _, item := range items {
		item = strings.Trim(item, "*#`_~")
		if strings.ContainsAny(item, "(){}[]<>|=+/") || len(item) == 0 {
			continue
		}
		out += fmt.Sprintf(`<span class="tag">%s</span>`, item)
	}
	return out
}

func renderFallbackEvents(events []FallbackEvent) string {
	if len(events) == 0 {
		return `<p class="empty">No fallbacks this session</p>`
	}
	out := ""
	for _, e := range events {
		reason := e.Reason
		if reason == "" {
			reason = "unknown"
		}
		out += fmt.Sprintf(`<div class="fb">
  <span class="fb-icon">&#x26A1;</span>
  <div style="flex:1">
    <div class="fb-title">Provider switched <span class="fb-time">%s</span></div>
    <div class="fb-detail" style="margin-bottom:4px">
      <span class="pill from">%s</span>
      <span style="color:#484f58;margin:0 5px">&#x2192;</span>
      <span class="pill to">%s</span>
    </div>
    <div class="fb-detail">Reason: %s</div>
  </div>
</div>`, e.At.Format("15:04:05"), e.FromProvider, e.ToProvider, reason)
	}
	return out
}

func renderOpenLoopsNew(loops []string) string {
	if len(loops) == 0 {
		return `<p class="empty">No open loops detected</p>`
	}
	out := ""
	for _, l := range loops {
		l = strings.Trim(l, "*#`_~0123456789. ")
		if l == "" {
			continue
		}
		out += fmt.Sprintf(`<div class="loop open"><span class="loop-dot open"></span><span class="loop-text open">%s</span></div>`, l)
	}
	return out
}

func renderResolvedLoops(loops []string) string {
	if len(loops) == 0 {
		return `<p class="empty">No resolved loops yet</p>`
	}
	out := ""
	for _, l := range loops {
		l = strings.Trim(l, "*#`_~0123456789. ")
		if l == "" {
			continue
		}
		out += fmt.Sprintf(`<div class="loop resolved"><span class="loop-dot resolved"></span><span class="loop-text resolved">%s</span></div>`, l)
	}
	return out
}

func renderCompleted(steps []string) string {
	if len(steps) == 0 {
		return `<p class="empty">No completed steps tracked yet</p>`
	}
	out := ""
	for i, s := range steps {
		out += fmt.Sprintf(`<div class="step"><span class="snum">%02d</span><span style="color:#c9d1d9;line-height:1.4">%s</span><span class="scheck">&#x2713;</span></div>`, i+1, s)
	}
	return out
}

func renderTranscript(entries []MessageEntry) string {
	if len(entries) == 0 {
		return `<p class="empty">No messages yet</p>`
	}
	lastProvider := ""
	out := ""
	for _, e := range entries {
		ts := e.At.Format("15:04:05")
		content := e.Content
		if len(content) > 600 {
			content = content[:600] + "..."
		}

		if e.Role == "fallback" {
			out += fmt.Sprintf(`<div class="turn fallback-event">
  <div class="turn-header"><span class="trole fallback">&#x26A1; Fallback</span><span class="ttime">%s</span></div>
  <div class="turn-text fb">%s &#x2192; %s</div>
</div>`, ts, e.Provider, content)
			continue
		}

		providerBadge := ""
		if e.Role == "assistant" && e.Provider != "" {
			cls := "tprovider"
			if e.Provider != lastProvider && lastProvider != "" {
				cls = "tprovider new"
			}
			providerBadge = fmt.Sprintf(`<span class="%s">%s</span>`, cls, e.Provider)
			lastProvider = e.Provider
		}

		roleClass := e.Role
		roleLabel := strings.ToUpper(e.Role)
		out += fmt.Sprintf(`<div class="turn %s">
  <div class="turn-header"><span class="trole %s">%s</span>%s<span class="ttime">%s</span></div>
  <div class="turn-text">%s</div>
</div>`, roleClass, roleClass, roleLabel, providerBadge, ts, content)
	}
	return out
}
