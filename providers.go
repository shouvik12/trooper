package main

import (
	"bytes"
	"log"
	"net/http"
	"sync"
	"time"
)

// ── Provider ──────────────────────────────────────────────────────────────────

type Provider struct {
	Name       string
	URL        string
	APIKey     string
	AuthHeader string
	Model      string
}

// ── Smart Chain ───────────────────────────────────────────────────────────────

func buildChain() []Provider {
	chain := []Provider{}

	// Claude — primary
	if key := getEnv("CLAUDE_API_KEY", getEnv("PRIMARY_API_KEY", "")); key != "" {
		chain = append(chain, Provider{
			Name:       "claude",
			URL:        getEnv("CLAUDE_URL", "https://api.anthropic.com/v1/messages"),
			APIKey:     key,
			AuthHeader: "x-api-key",
		})
	}

	// Gemini — optional cloud fallback, user opt-in
	if key := getEnv("GEMINI_API_KEY", ""); key != "" {
		chain = append(chain, Provider{
			Name:       "gemini",
			URL:        getEnv("GEMINI_URL", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"),
			APIKey:     key,
			AuthHeader: "Authorization",
			Model:      getEnv("GEMINI_MODEL", "gemini-2.0-flash"),
		})
	}

	// OpenAI — optional cloud fallback, user opt-in
	if key := getEnv("OPENAI_API_KEY", ""); key != "" {
		chain = append(chain, Provider{
			Name:       "openai",
			URL:        getEnv("OPENAI_URL", "https://api.openai.com/v1/chat/completions"),
			APIKey:     key,
			AuthHeader: "Authorization",
			Model:      getEnv("OPENAI_MODEL", "gpt-4o-mini"),
		})
	}

	// Ollama — always last, always local, privacy safety net
	chain = append(chain, Provider{
		Name:  "ollama",
		URL:   getEnv("FALLBACK_URL", "http://localhost:11434/api/chat"),
		Model: getEnv("OLLAMA_MODEL", "qwen2.5:3b"),
	})

	return chain
}

// ── Active Provider ───────────────────────────────────────────────────────────

type ActiveProvider struct {
	mu    sync.RWMutex
	index int
}

func (a *ActiveProvider) Get() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.index
}

func (a *ActiveProvider) Set(index int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.index = index
}

func startHealthCheck(chain []Provider, active *ActiveProvider) {
	if getEnv("AUTO_RECOVERY", "false") != "true" {
		log.Printf("🏥 Auto recovery disabled — set AUTO_RECOVERY=true to enable")
		return
	}

	go func() {
		log.Printf("🏥 Auto recovery enabled — checking every 60 seconds")
		for {
			time.Sleep(60 * time.Second)
			log.Printf("🏥 Health check running...")
			for i, p := range chain {
				if p.Name == "ollama" {
					continue
				}
				if p.APIKey == "" {
					continue
				}
				body := `{"model":"` + p.Model + `","max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`
				resp, err := http.Post(p.URL, "application/json", bytes.NewBufferString(body))
				if err != nil {
					continue
				}
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					current := active.Get()
					if i < current {
						log.Printf("🔄 Auto recovery — switching back to %s", p.Name)
						active.Set(i)
					}
					break
				}
			}
		}
	}()
}

// ── Session Store ─────────────────────────────────────────────────────────────

type SessionStore struct {
	mu       sync.Mutex
	sessions map[string][]map[string]string
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string][]map[string]string),
	}
}

func (s *SessionStore) Append(sessionID string, messages []map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.sessions[sessionID]
	s.sessions[sessionID] = append(existing, messages...)
}

func (s *SessionStore) Get(sessionID string) []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[sessionID]
}
