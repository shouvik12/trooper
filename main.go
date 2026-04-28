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

	cfg := loadConfig()
	log.Printf("🪖  Trooper proxy starting on http://localhost:%s", port)
	log.Printf("    Primary  : %s", cfg.PrimaryURL)
	log.Printf("    Fallback : %s (%s)", cfg.FallbackURL, cfg.FallbackModel)
	log.Printf("    Triggers : HTTP %v", cfg.QuotaStatusCodes)

	http.HandleFunc("/", makeHandler(cfg))
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// ── Config ────────────────────────────────────────────────────────────────────

type Config struct {
	PrimaryURL        string
	PrimaryAPIKey     string
	PrimaryAuthHeader string // "x-api-key" for Claude, "Authorization" for OpenAI/others
	FallbackURL       string
	FallbackModel     string
	QuotaStatusCodes  map[int]bool
}

func loadConfig() Config {
	quotaCodes := map[int]bool{}
	raw := getEnv("QUOTA_STATUS_CODES", "429,402,529,400")
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if code, err := strconv.Atoi(s); err == nil {
			quotaCodes[code] = true
		}
	}

	return Config{
		PrimaryURL:        getEnv("PRIMARY_URL", "https://api.anthropic.com/v1/messages"),
		PrimaryAPIKey:     getEnv("PRIMARY_API_KEY", os.Getenv("ANTHROPIC_API_KEY")),
		PrimaryAuthHeader: getEnv("PRIMARY_AUTH_HEADER", "x-api-key"),
		FallbackURL:       getEnv("FALLBACK_URL", "http://localhost:11434/api/chat"),
		FallbackModel:     getEnv("FALLBACK_MODEL", getEnv("OLLAMA_MODEL", "qwen2.5:3b")),
		QuotaStatusCodes:  quotaCodes,
	}
}

// ── Handler ───────────────────────────────────────────────────────────────────

func makeHandler(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.PrimaryAPIKey == "" {
			http.Error(w, `{"error":"PRIMARY_API_KEY not set"}`, http.StatusInternalServerError)
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

		log.Printf("📥 %s %s (stream=%v)", r.Method, r.URL.Path, wantsStream)

		// 1. Try primary
		primaryResp, err := callPrimary(body, r, cfg)
		if err == nil {
			switch {
			case primaryResp.StatusCode == http.StatusOK:
				log.Printf("✅ Primary responded OK")
				copyResponse(w, primaryResp)
				return

			case primaryResp.StatusCode == http.StatusUnauthorized:
				log.Printf("❌ Primary 401 — bad API key, not falling back")
				copyResponse(w, primaryResp)
				return

			case primaryResp.StatusCode == 429:
				log.Printf("⚠️  Primary 429 — rate limited, retrying with backoff")
				io.Copy(io.Discard, primaryResp.Body)
				primaryResp.Body.Close()
				time.Sleep(2 * time.Second)
				primaryResp, err = callPrimary(body, r, cfg)
				if err == nil && primaryResp.StatusCode == http.StatusOK {
					log.Printf("✅ Primary recovered after backoff")
					copyResponse(w, primaryResp)
					return
				}
				log.Printf("⚠️  Primary still failing after backoff — falling back")
				io.Copy(io.Discard, primaryResp.Body)
				primaryResp.Body.Close()

			case primaryResp.StatusCode == 402:
				log.Printf("⚠️  Primary 402 — credits gone, falling back immediately")
				io.Copy(io.Discard, primaryResp.Body)
				primaryResp.Body.Close()

			case primaryResp.StatusCode == 400:
				bodyBytes, _ := io.ReadAll(primaryResp.Body)
				primaryResp.Body.Close()
				if strings.Contains(string(bodyBytes), "credit balance") {
					log.Printf("⚠️  Primary 400 — credit balance too low, falling back immediately")
				} else {
					log.Printf("❌ Primary 400 — bad request, not falling back")
					w.WriteHeader(400)
					w.Write(bodyBytes)
					return
				}
			case cfg.QuotaStatusCodes[primaryResp.StatusCode]:
				log.Printf("⚠️  Primary %d — falling back to local model", primaryResp.StatusCode)
				io.Copy(io.Discard, primaryResp.Body)
				primaryResp.Body.Close()

			default:
				log.Printf("❌ Primary %d — non-recoverable", primaryResp.StatusCode)
				copyResponse(w, primaryResp)
				return
			}
		} else {
			log.Printf("⚠️  Primary network error: %v — falling back", err)
		}

		// 2. Fallback to local model
		log.Printf("🪖  Routing to local model: %s", cfg.FallbackModel)

		if wantsStream {
			streamFallback(w, body, cfg)
		} else {
			fallbackResp, err := callFallback(body, cfg)
			if err != nil {
				log.Printf("❌ Fallback error: %v", err)
				http.Error(w, `{"error":"both primary and local model failed"}`, http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Trooper-Fallback", cfg.FallbackModel)
			w.Write(fallbackResp)
		}
	}
}

// ── Primary ───────────────────────────────────────────────────────────────────

func callPrimary(body []byte, r *http.Request, cfg Config) (*http.Response, error) {
	req, err := http.NewRequest("POST", cfg.PrimaryURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}

	for k, v := range r.Header {
		req.Header[k] = v
	}

	if strings.ToLower(cfg.PrimaryAuthHeader) == "authorization" {
		req.Header.Set("Authorization", "Bearer "+cfg.PrimaryAPIKey)
	} else {
		req.Header.Set(cfg.PrimaryAuthHeader, cfg.PrimaryAPIKey)
	}

	if strings.Contains(cfg.PrimaryURL, "anthropic.com") {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	return http.DefaultClient.Do(req)
}

// ── Fallback (Ollama / any local OpenAI-compatible server) ───────────────────

func callFallback(body []byte, cfg Config) ([]byte, error) {
	messages, err := extractMessages(body)
	if err != nil {
		return nil, fmt.Errorf("extracting messages: %w", err)
	}

	ollamaReq := map[string]interface{}{
		"model":    cfg.FallbackModel,
		"messages": messages,
		"stream":   false,
	}
	reqBytes, _ := json.Marshal(ollamaReq)

	resp, err := http.Post(cfg.FallbackURL, "application/json", bytes.NewBuffer(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("fallback unreachable at %s: %w", cfg.FallbackURL, err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)

	var parsed map[string]interface{}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, fmt.Errorf("fallback response parse error: %w", err)
	}

	text := extractFallbackText(parsed)
	return wrapAsOpenAI(text, cfg.FallbackModel), nil
}

func streamFallback(w http.ResponseWriter, body []byte, cfg Config) {
	messages, err := extractMessages(body)
	if err != nil {
		http.Error(w, `{"error":"failed to parse messages"}`, http.StatusBadRequest)
		return
	}

	ollamaReq := map[string]interface{}{
		"model":    cfg.FallbackModel,
		"messages": messages,
		"stream":   true,
	}
	reqBytes, _ := json.Marshal(ollamaReq)

	resp, err := http.Post(cfg.FallbackURL, "application/json", bytes.NewBuffer(reqBytes))
	if err != nil {
		http.Error(w, `{"error":"fallback unreachable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Trooper-Fallback", cfg.FallbackModel)

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
	// Ollama /api/chat shape
	if msg, ok := parsed["message"].(map[string]interface{}); ok {
		if text, ok := msg["content"].(string); ok {
			return text
		}
	}
	// Ollama /api/generate shape
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

// wrapAsOpenAI wraps text in a response envelope compatible with both
// OpenAI and Claude SDK callers.
func wrapAsOpenAI(text string, model string) []byte {
	resp := map[string]interface{}{
		"id":     "trooper-fallback",
		"object": "chat.completion",
		"model":  model,
		// OpenAI-compatible shape
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": text},
				"finish_reason": "stop",
			},
		},
		// Claude-compatible shape (for anthropic SDK callers)
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
