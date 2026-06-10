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
	Anchor            []map[string]string // Turns 1-2 (Immortal context)
	SITREP            string              // Rolling distilled summary
	Tail              []map[string]string // Recent turns
	TokensSaved       int                 // cumulative tokens saved by routing to Ollama
	FullHistoryTokens int                 // tokens in full history before compression
	CompressedTokens  int                 // tokens actually sent after SITREP compression
	TotalTokensSeen   int                 // running total of all tokens ever seen in session
	LastSeen          time.Time
	mu                sync.Mutex

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
		// Track total tokens seen for compression ratio
		msgJSON, _ := json.Marshal(msg)
		state.TotalTokensSeen += len(msgJSON) / 4
	}

	state.LastSeen = time.Now()

	if len(state.Tail) > TailThreshold {
		compressCount := 5
		if len(state.Tail) < compressCount {
			compressCount = len(state.Tail)
		}
		toCompress := make([]map[string]string, compressCount)
		copy(toCompress, state.Tail[:compressCount])
		state.Tail = state.Tail[compressCount:]
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
		// Track total tokens seen for compression ratio
		msgJSON, _ := json.Marshal(msg)
		state.TotalTokensSeen += len(msgJSON) / 4
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

	// Filter to user messages only — don't let Qwen see assistant responses
	var userTurns []map[string]string
	for _, turn := range newTurns {
		if turn["role"] == "user" {
			userTurns = append(userTurns, turn)
		}
	}
	if len(userTurns) == 0 {
		return
	}
	turnsJSON, _ := json.Marshal(userTurns)

	prompt := fmt.Sprintf(`You are a memory extractor for an AI agent proxy.
From the user turns below, extract only what the agent needs to keep working.

Output in this exact format:
INTENT: 
DECISIONS: 
CONSTRAINTS:
OPEN: 
ENTITIES: 

Current SITREP:
%s

New turns:
%s

Output only the updated SITREP. Nothing else.`, state.SITREP, string(turnsJSON))

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

	// Measure full history tokens
	var fullHistory []map[string]string
	fullHistory = append(fullHistory, state.Anchor...)
	fullHistory = append(fullHistory, state.Tail...)
	fullJSON, _ := json.Marshal(fullHistory)
	state.FullHistoryTokens = len(fullJSON) / 4

	// Build compressed payload
	payload := append([]map[string]string{}, state.Anchor...)
	if state.SITREP != "" {
		payload = append(payload, map[string]string{
			"role":    "user",
			"content": fmt.Sprintf("[STATE_SITREP: %s]", state.SITREP),
		})
	}
	payload = append(payload, state.Tail...)
	compressedJSON, _ := json.Marshal(payload)
	state.CompressedTokens = len(compressedJSON) / 4

	return payload
}

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
	seen := make(map[string]bool)
	for _, e := range state.entries {
		key := e.Role + "||" + e.Content
		if seen[key] {
			continue
		}
		seen[key] = true
		content := e.Content
		// Provider tags removed — causes model confusion when reading history
		all = append(all, map[string]string{
			"role":    e.Role,
			"content": content,
		})
	}
	return all
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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

// GetCompressionRatio returns the percentage of tokens saved via SITREP compression
func (s *SessionStore) GetCompressionRatio(sessionID string) string {
	s.mu.RLock()
	state, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return "0%"
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.TotalTokensSeen == 0 {
		return "—"
	}
	saved := float64(state.TotalTokensSeen-state.CompressedTokens) / float64(state.TotalTokensSeen) * 100
	if saved < 0 {
		saved = 0
	}
	return fmt.Sprintf("%.0f%%", saved)
}

// GetResolvedLoops extracts resolved decisions and constraints from structured SITREP
func (s *SessionStore) GetResolvedLoops(sessionID string) []string {
	s.mu.RLock()
	state, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.SITREP == "" {
		return nil
	}

	var loops []string
	inDecisions := false
	inConstraints := false

	for _, line := range strings.Split(state.SITREP, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "DECISIONS:") {
			inDecisions = true
			inConstraints = false
			content := strings.TrimSpace(strings.TrimPrefix(line, "DECISIONS:"))
			if content != "" {
				loops = append(loops, content)
			}
			continue
		}
		if strings.HasPrefix(line, "CONSTRAINTS:") {
			inConstraints = true
			inDecisions = false
			content := strings.TrimSpace(strings.TrimPrefix(line, "CONSTRAINTS:"))
			if content != "" {
				loops = append(loops, content)
			}
			continue
		}
		if strings.HasPrefix(line, "INTENT:") ||
			strings.HasPrefix(line, "OPEN:") ||
			strings.HasPrefix(line, "ENTITIES:") {
			inDecisions = false
			inConstraints = false
			continue
		}
		if inDecisions || inConstraints {
			clean := strings.TrimLeft(line, "-• ")
			if clean != "" {
				loops = append(loops, clean)
			}
		}
	}

	return loops
}

// GetRawSITREP returns the raw structured SITREP string for display
func (s *SessionStore) GetRawSITREP(sessionID string) string {
	s.mu.RLock()
	state, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return "No SITREP yet"
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.SITREP == "" {
		return "Building..."
	}
	return state.SITREP
}

// HasSITREP returns true if the session has a non-empty SITREP
func (s *SessionStore) HasSITREP(sessionID string) bool {
	s.mu.RLock()
	state, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.SITREP != ""
}

// BuildCompressedBody rewrites the request body with compressed history (Anchor + SITREP + Tail)
func (s *SessionStore) BuildCompressedBody(sessionID string, originalBody []byte) []byte {
	history := s.GetTripleAnchor(sessionID)
	if len(history) == 0 {
		return nil
	}
	var reqMap map[string]interface{}
	if err := json.Unmarshal(originalBody, &reqMap); err != nil {
		return nil
	}
	reqMap["messages"] = history
	compressed, err := json.Marshal(reqMap)
	if err != nil {
		return nil
	}
	return compressed
}

// folderModel returns the Ollama model used for SITREP distillation
func folderModel() string {
	return getEnv("SITREP_MODEL", getEnv("OLLAMA_MODEL", "qwen2.5:3b"))
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
			Model:      getEnv("CLAUDE_MODEL", "claude-haiku-4-5-20251001"),
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
