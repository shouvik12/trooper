package main

import "sync"

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
