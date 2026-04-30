package main

import (
	"bytes"
	"log"
	"net/http"
	"strings"
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
				body := `{"model":"` + p.Model + `","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`
				req, err := http.NewRequest("POST", p.URL, bytes.NewBufferString(body))
				if err != nil {
					continue
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
				client := &http.Client{Timeout: 10 * time.Second}
				resp, err := client.Do(req)
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

type Session struct {
	Messages []map[string]string
	LastSeen time.Time
}

type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func NewSessionStore() *SessionStore {
	s := &SessionStore{
		sessions: make(map[string]*Session),
	}
	go s.cleanup()
	return s
}

func (s *SessionStore) cleanup() {
	for {
		time.Sleep(10 * time.Minute)
		s.mu.Lock()
		for id, session := range s.sessions {
			if time.Since(session.LastSeen) > 24*time.Hour {
				log.Printf("🧹 Session expired: %s", id)
				delete(s.sessions, id)
			}
		}
		s.mu.Unlock()
	}
}

func (s *SessionStore) Append(sessionID string, messages []map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		s.sessions[sessionID] = &Session{}
	}
	s.sessions[sessionID].Messages = append(s.sessions[sessionID].Messages, messages...)
	s.sessions[sessionID].LastSeen = time.Now()
}

func (s *SessionStore) Get(sessionID string) []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[sessionID]; ok {
		return session.Messages
	}
	return nil
}
