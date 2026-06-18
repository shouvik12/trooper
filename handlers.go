package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// ── Handler ───────────────────────────────────────────────────────────────────

func makeHandler(chain []Provider, quotaCodes map[int]bool, active *ActiveProvider, store *SessionStore, states map[string]*ProviderState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || (r.Method == http.MethodGet && r.URL.Path == "/") {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Ignore non-API requests — prevents Chrome DevTools noise from hitting provider chain
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			http.NotFound(w, r)
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
		if r.Header.Get("X-Force-Local") == "true" {
			forceLocal = true
		}

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
