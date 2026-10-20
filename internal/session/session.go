// Package session keeps PostgreSQL credentials in memory for the lifetime of a
// browser session.
//
// Credentials are never written to disk, never logged and never returned to the
// browser. They live only as long as the session is used, so a dump can be
// started later without asking the operator to retype the password.
package session

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/postgres"
)

// DefaultTTL is how long an idle session is kept.
const DefaultTTL = 30 * time.Minute

// CookieName is the session cookie name.
const CookieName = "pgdd_session"

// Credentials are the PostgreSQL credentials supplied through the UI.
type Credentials struct {
	User     string
	Password execution.Secret
	Database string
}

// Session is one authenticated dashboard session.
type Session struct {
	ID       string
	Created  time.Time
	lastSeen time.Time
	Creds    Credentials

	mu       sync.RWMutex
	overview *postgres.Overview
}

// Overview returns the last discovery result.
func (s *Session) Overview() *postgres.Overview {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.overview
}

// SetOverview stores a discovery result.
func (s *Session) SetOverview(o *postgres.Overview) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overview = o
}

// ExpiresAt reports when the session will expire unless it is used again.
func (s *Session) ExpiresAt(ttl time.Duration) time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSeen.Add(ttl)
}

// Database finds one database in the cached overview.
func (s *Session) Database(name string) (postgres.Database, bool) {
	overview := s.Overview()
	if overview == nil {
		return postgres.Database{}, false
	}
	for _, db := range overview.Databases {
		if db.Name == name {
			return db, true
		}
	}
	return postgres.Database{}, false
}

// Store is an in-memory session store.
type Store struct {
	ttl time.Duration
	now func() time.Time

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewStore returns a store with the given idle TTL.
func NewStore(ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Store{ttl: ttl, now: time.Now, sessions: make(map[string]*Session)}
}

// TTL returns the configured idle timeout.
func (s *Store) TTL() time.Duration { return s.ttl }

// Create registers a new session and returns it.
func (s *Store) Create(creds Credentials) *Session {
	now := s.now()
	sess := &Session{ID: newID(), Created: now, lastSeen: now, Creds: creds}

	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()

	return sess
}

// Get returns a live session and marks it as used.
func (s *Store) Get(id string) (*Session, bool) {
	if id == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	if s.expired(sess) {
		delete(s.sessions, id)
		return nil, false
	}
	sess.mu.Lock()
	sess.lastSeen = s.now()
	sess.mu.Unlock()
	return sess, true
}

// Delete removes a session.
func (s *Store) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// Len reports the number of live sessions.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// GC drops expired sessions and returns how many were removed.
func (s *Store) GC() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	for id, sess := range s.sessions {
		if s.expired(sess) {
			delete(s.sessions, id)
			removed++
		}
	}
	return removed
}

// RunGC periodically removes expired sessions until ctx is done.
func (s *Store) RunGC(ctx interface {
	Done() <-chan struct{}
}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.GC()
		}
	}
}

func (s *Store) expired(sess *Session) bool {
	sess.mu.RLock()
	last := sess.lastSeen
	sess.mu.RUnlock()
	return s.now().Sub(last) > s.ttl
}

func newID() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure means the process cannot be trusted.
		panic("session: cannot read random bytes: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
