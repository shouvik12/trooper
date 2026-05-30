package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ── Constants ─────────────────────────────────────────────────────────────────

const TailThreshold = 10 // Move oldest 5 turns to SITREP when Tail exceeds this

// ── Types ─────────────────────────────────────────────────────────────────────

type Provider struct {
	Name       string
	URL        string
	APIKey     string
	Model      string
	AuthHeader string
}

type ActiveProvider struct {
	index int
	mu    sync.RWMutex
}

func (a *ActiveProvider) Get() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.index
}

func (a *ActiveProvider) Set(i int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.index = i
}

// ── New types for dashboard ───────────────────────────────────────────────────

type MessageEntry struct {
	Role     string
	Content  string
	Provider string
	At       time.Time
}

type FallbackEvent struct {
	FromProvider string
	ToProvider   string
	Reason       string
	At           time.Time
}

// ── Rolling SITREP Structures ─────────────────────────────────────────────────

type SessionState struct {
	Anchor      []map[string]string // Turns 1-2 (Immortal context)
	SITREP      string              // Rolling distilled summary
	Tail        []map[string]string // Recent turns
	TokensSaved int                 // cumulative tokens saved by routing to Ollama
	LastSeen    time.Time
	mu          sync.Mutex

	// Dashboard fields
	entries   []MessageEntry
	fallbacks []FallbackEvent
	startTime time.Time
}

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*SessionState
}

func NewSessionStore() *SessionStore {
	s := &SessionStore{sessions: make(map[string]*SessionState)}
	go s.cleanup()
	return s
}

func (s *SessionStore) cleanup() {
	for {
		time.Sleep(10 * time.Minute)
		s.mu.Lock()
		for id, state := range s.sessions {
			if time.Since(state.LastSeen) > 24*time.Hour {
				log.Printf("🧹 Session expired: %s", id)
				delete(s.sessions, id)
			}
		}
		s.mu.Unlock()
	}
}

func (s *SessionStore) AddTokensSaved(sessionID string, tokens int) int {
	s.mu.RLock()
	state, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.TokensSaved += tokens
	return state.TokensSaved
}

// Append manages the rolling window: moving older Tail turns into SITREP
func (s *SessionStore) Append(sessionID string, messages []map[string]string) {
	s.mu.Lock()
	state, ok := s.sessions[sessionID]
	if !ok {
		state = &SessionState{
			LastSeen:  time.Now(),
			startTime: time.Now(),
		}
		s.sessions[sessionID] = state
	}
	s.mu.Unlock()

	state.mu.Lock()
	defer state.mu.Unlock()

	for _, msg := range messages {
		if len(state.Anchor) < 2 {
			state.Anchor = append(state.Anchor, msg)
		} else {
			state.Tail = append(state.Tail, msg)
		}
		// Also record in entries for dashboard transcript
		state.entries = append(state.entries, MessageEntry{
			Role:    msg["role"],
			Content: msg["content"],
			At:      time.Now(),
		})
	}

	state.LastSeen = time.Now()

	if len(state.Tail) > TailThreshold {
		toCompress := make([]map[string]string, 5)
		copy(toCompress, state.Tail[:5])
		state.Tail = state.Tail[5:]
		go s.updateSITREP(sessionID, toCompress)
	}
}

// AppendWithMeta stores a message entry with provider and timestamp metadata
func (s *SessionStore) AppendWithMeta(sessionID string, entries []MessageEntry) {
	s.mu.Lock()
	state, ok := s.sessions[sessionID]
	if !ok {
		state = &SessionState{
			LastSeen:  time.Now(),
			startTime: time.Now(),
		}
		s.sessions[sessionID] = state
	}
	s.mu.Unlock()

	state.mu.Lock()
	defer state.mu.Unlock()

	for _, e := range entries {
		if e.At.IsZero() {
			e.At = time.Now()
		}
		state.entries = append(state.entries, e)
		// Keep plain sessions map in sync for SITREP / compaction
		msg := map[string]string{"role": e.Role, "content": e.Content}
		if len(state.Anchor) < 2 {
			state.Anchor = append(state.Anchor, msg)
		} else {
			state.Tail = append(state.Tail, msg)
		}
	}
	state.LastSeen = time.Now()
}

// GetEntries returns all transcript entries for a session
func (s *SessionStore) GetEntries(sessionID string) []MessageEntry {
	s.mu.RLock()
	state, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	result := make([]MessageEntry, len(state.entries))
	copy(result, state.entries)
	return result
}

// AddFallbackEvent records a provider switch and injects it into the transcript
func (s *SessionStore) AddFallbackEvent(sessionID string, event FallbackEvent) {
	s.mu.Lock()
	state, ok := s.sessions[sessionID]
	if !ok {
		state = &SessionState{
			LastSeen:  time.Now(),
			startTime: time.Now(),
		}
		s.sessions[sessionID] = state
	}
	s.mu.Unlock()

	state.mu.Lock()
	defer state.mu.Unlock()

	state.fallbacks = append(state.fallbacks, event)

	// Inject synthetic transcript entry so it appears inline
	state.entries = append(state.entries, MessageEntry{
		Role:     "fallback",
		Content:  event.ToProvider,
		Provider: event.FromProvider,
		At:       event.At,
	})
}

// GetFallbackEvents returns all fallback events for a session
func (s *SessionStore) GetFallbackEvents(sessionID string) []FallbackEvent {
	s.mu.RLock()
	state, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	result := make([]FallbackEvent, len(state.fallbacks))
	copy(result, state.fallbacks)
	return result
}

// GetStartTime returns when the session started
func (s *SessionStore) GetStartTime(sessionID string) time.Time {
	s.mu.RLock()
	state, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return time.Time{}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.startTime
}

// updateSITREP performs incremental distillation using Ollama
func (s *SessionStore) updateSITREP(sessionID string, newTurns []map[string]string) {
	s.mu.RLock()
	state, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return
	}

	turnsJSON, _ := json.Marshal(newTurns)
	prompt := fmt.Sprintf(`Update SITREP. Protocol: "direct" (v2).

Rules: No filler, use word-level abbreviations, technical acronyms only.

Current SITREP: %s

New History to integrate: %s`, state.SITREP, string(turnsJSON))

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":  folderModel(),
		"prompt": prompt,
		"stream": false,
	})

	resp, err := http.Post(
		getEnv("OLLAMA_BASE_URL", "http://localhost:11434")+"/api/generate",
		"application/json",
		bytes.NewBuffer(reqBody),
	)
	if err != nil {
		log.Printf("⚠️ SITREP distillation unreachable: %v", err)
		return
	}
	defer resp.Body.Close()

	var res struct {
		Response string `json:"response"`
	}
	json.NewDecoder(resp.Body).Decode(&res)

	state.mu.Lock()
	state.SITREP = res.Response
	state.mu.Unlock()

	log.Printf("📡 SITREP updated for session: %s", sessionID)
}

// GetTripleAnchor assembles the sandwich: Anchor + [SITREP] + Tail
func (s *SessionStore) GetTripleAnchor(sessionID string) []map[string]string {
	s.mu.RLock()
	state, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	payload := append([]map[string]string{}, state.Anchor...)
	if state.SITREP != "" {
		payload = append(payload, map[string]string{
			"role":    "system",
			"content": fmt.Sprintf("[STATE_SITREP: %s]", state.SITREP),
		})
	}
	return append(payload, state.Tail...)
}

// GetAll returns all messages flat for a session
func (s *SessionStore) GetAll(sessionID string) []map[string]string {
	s.mu.RLock()
	state, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	var all []map[string]string
	all = append(all, state.Anchor...)
	all = append(all, state.Tail...)
	return all
}

// GetTokensSaved returns cumulative tokens saved for a session
func (s *SessionStore) GetTokensSaved(sessionID string) int {
	s.mu.RLock()
	state, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.TokensSaved
}

// folderModel returns the Ollama model used for SITREP distillation
func folderModel() string {
	return getEnv("OLLAMA_MODEL", "qwen2.5:3b")
}

// ── Health Check URL ──────────────────────────────────────────────────────────

func healthCheckURL(p Provider) string {
	switch p.Name {
	case "claude":
		return "https://api.anthropic.com/v1/models"
	case "openai":
		return "https://api.openai.com/v1/models"
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta/models?key=" + p.APIKey
	default:
		return ""
	}
}

// ── Provider Setup ────────────────────────────────────────────────────────────

func buildChain() []Provider {
	var chain []Provider

	if key := getEnv("CLAUDE_API_KEY", getEnv("PRIMARY_API_KEY", "")); key != "" {
		chain = append(chain, Provider{
			Name:       "claude",
			URL:        getEnv("CLAUDE_URL", "https://api.anthropic.com/v1/messages"),
			APIKey:     key,
			AuthHeader: "x-api-key",
			Model:      getEnv("CLAUDE_MODEL", "claude-3-5-haiku-20241022"),
		})
	}

	if key := getEnv("GEMINI_API_KEY", ""); key != "" {
		chain = append(chain, Provider{
			Name:       "gemini",
			URL:        getEnv("GEMINI_URL", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"),
			APIKey:     key,
			AuthHeader: "Authorization",
			Model:      getEnv("GEMINI_MODEL", "gemini-2.0-flash"),
		})
	}

	if key := getEnv("OPENAI_API_KEY", ""); key != "" {
		chain = append(chain, Provider{
			Name:       "openai",
			URL:        getEnv("OPENAI_URL", "https://api.openai.com/v1/chat/completions"),
			APIKey:     key,
			AuthHeader: "Authorization",
			Model:      getEnv("OPENAI_MODEL", "gpt-4o-mini"),
		})
	}

	chain = append(chain, Provider{
		Name:  "ollama",
		URL:   getEnv("FALLBACK_URL", "http://localhost:11434/api/chat"),
		Model: getEnv("OLLAMA_MODEL", "qwen2.5:3b"),
	})

	return chain
}

// ── Health Check ──────────────────────────────────────────────────────────────

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

				url := healthCheckURL(p)
				if url == "" {
					continue
				}

				var req *http.Request
				var err error

				if p.Name == "gemini" {
					req, err = http.NewRequest("GET", url, nil)
				} else {
					req, err = http.NewRequest("GET", url, nil)
					if err != nil {
						continue
					}
					if strings.ToLower(p.AuthHeader) == "authorization" {
						req.Header.Set("Authorization", "Bearer "+p.APIKey)
					} else if p.AuthHeader != "" {
						req.Header.Set(p.AuthHeader, p.APIKey)
					}
					if p.Name == "claude" {
						req.Header.Set("anthropic-version", "2023-06-01")
					}
				}

				if err != nil {
					continue
				}

				client := &http.Client{Timeout: 10 * time.Second}
				resp, err := client.Do(req)
				if err != nil {
					log.Printf("🏥 %s unreachable", p.Name)
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
				} else {
					log.Printf("🏥 %s health check failed: %d", p.Name, resp.StatusCode)
				}
			}
		}
	}()
}
