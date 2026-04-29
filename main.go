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
	"time"
)

func main() {
	port := getEnv("TROOPER_PORT", "3000")

	chain := buildChain()
	quotaCodes := loadQuotaCodes()
	active := &ActiveProvider{index: 0}
	startHealthCheck(chain, active)

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
	log.Printf("🪖  Trooper proxy starting on http://localhost:%s", port)
	for i, p := range chain {
		log.Printf("    Provider %d: %s", i+1, p.Name)
	}
	log.Printf("    Triggers : HTTP %v", quotaCodes)

	http.HandleFunc("/", makeHandler(chain, quotaCodes, active))
	if err := http.ListenAndServe(":"+port, nil); err != nil {
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

func makeHandler(chain []Provider, quotaCodes map[int]bool, active *ActiveProvider) http.HandlerFunc {
	store := NewSessionStore()

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

		history := store.Get(sessionID)
		fallbackCount := 0
		trigger := ""

		// Try each provider starting from active index
		for i := active.Get(); i < len(chain); i++ {
			provider := chain[i]
			log.Printf("🔄 Trying provider: %s", provider.Name)

			if provider.Name == "ollama" {
				log.Printf("🪖  Routing to local model: %s", provider.Model)
				w.Header().Set("X-Trooper-Provider", "ollama")
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
				active.Set(i + 1)
				continue
			}

			switch {
			case resp.StatusCode == http.StatusOK:
				log.Printf("✅ %s responded OK", provider.Name)
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
				active.Set(i + 1)
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
				active.Set(i + 1)
				continue

			case resp.StatusCode == 402:
				log.Printf("⚠️  %s 402 — credits gone, trying next", provider.Name)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				fallbackCount++
				trigger = "402"
				active.Set(i + 1)
				continue

			case resp.StatusCode == 400:
				bodyBytes, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if strings.Contains(string(bodyBytes), "credit balance") {
					log.Printf("⚠️  %s 400 — credit balance too low, trying next", provider.Name)
					fallbackCount++
					trigger = "credit_balance"
					active.Set(i + 1)
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
				active.Set(i + 1)
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
	json.Unmarshal(body, &reqMap)
	if p.Model != "" {
		reqMap["model"] = p.Model
	}
	newBody, _ := json.Marshal(reqMap)

	req, err := http.NewRequest("POST", p.URL, bytes.NewBuffer(newBody))
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
	messages := history
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
	messages := history
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
	body, _ := io.ReadAll(resp.Body)
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
