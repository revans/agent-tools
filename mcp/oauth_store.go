package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	accessTTL   = time.Hour
	refreshTTL  = 30 * 24 * time.Hour
	codeTTL     = 60 * time.Second
	pendingTTL  = 10 * time.Minute
	maxClients  = 500
	rotatedKeep = 90 * 24 * time.Hour // how long a spent refresh token is remembered, to catch reuse
)

// randomToken returns n random bytes as URL-safe base64.
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system's random source is gone; nothing safe can continue
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// hashOf is how secrets are stored: tokens carry 256 bits of randomness, so a
// plain SHA-256 is enough, and a stolen state file reveals no usable token.
func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

type oauthClient struct {
	ID         string    `json:"id"`
	SecretHash string    `json:"secret_hash,omitempty"` // empty for public clients
	Name       string    `json:"name"`
	Redirects  []string  `json:"redirects"`
	AuthMethod string    `json:"auth_method"` // none | client_secret_post | client_secret_basic
	Created    time.Time `json:"created"`
}

type accessToken struct {
	ClientID string    `json:"client_id"`
	Resource string    `json:"resource"`
	Family   string    `json:"family"`
	Expires  time.Time `json:"expires"`
}

type refreshToken struct {
	ClientID string    `json:"client_id"`
	Resource string    `json:"resource"`
	Family   string    `json:"family"`
	Expires  time.Time `json:"expires"`
}

type authCode struct {
	ClientID    string
	RedirectURI string
	Challenge   string
	Resource    string
	Expires     time.Time
}

type pendingAuth struct {
	ClientID    string
	RedirectURI string
	Challenge   string
	State       string
	Resource    string
	Expires     time.Time
}

// oauthStore keeps clients and tokens, and persists them to one JSON file
// (mode 0600, replaced atomically). Authorization codes and pending consent
// requests are short-lived and stay in memory only.
type oauthStore struct {
	mu   sync.Mutex
	path string
	now  func() time.Time

	Clients map[string]*oauthClient  `json:"clients"`
	Access  map[string]*accessToken  `json:"access"`  // by hashOf(token)
	Refresh map[string]*refreshToken `json:"refresh"` // by hashOf(token)
	Rotated map[string]rotatedMark   `json:"rotated"` // spent refresh tokens, by hash

	codes   map[string]*authCode
	pending map[string]*pendingAuth
}

type rotatedMark struct {
	Family  string    `json:"family"`
	Expires time.Time `json:"expires"`
}

func openOAuthStore(path string) (*oauthStore, error) {
	s := &oauthStore{
		path:    path,
		now:     time.Now,
		Clients: map[string]*oauthClient{},
		Access:  map[string]*accessToken{},
		Refresh: map[string]*refreshToken{},
		Rotated: map[string]rotatedMark{},
		codes:   map[string]*authCode{},
		pending: map[string]*pendingAuth{},
	}
	if path == "" {
		return s, nil // in memory, for tests
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, errors.New("oauth state file " + path + " is corrupt: " + err.Error())
	}
	if s.Clients == nil {
		s.Clients = map[string]*oauthClient{}
	}
	if s.Access == nil {
		s.Access = map[string]*accessToken{}
	}
	if s.Refresh == nil {
		s.Refresh = map[string]*refreshToken{}
	}
	if s.Rotated == nil {
		s.Rotated = map[string]rotatedMark{}
	}
	return s, nil
}

// saveLocked writes the state; the caller holds s.mu.
func (s *oauthStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".oauth-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// sweepLocked drops everything expired.
func (s *oauthStore) sweepLocked() {
	now := s.now()
	for k, v := range s.Access {
		if now.After(v.Expires) {
			delete(s.Access, k)
		}
	}
	for k, v := range s.Refresh {
		if now.After(v.Expires) {
			delete(s.Refresh, k)
		}
	}
	for k, v := range s.Rotated {
		if now.After(v.Expires) {
			delete(s.Rotated, k)
		}
	}
	for k, v := range s.codes {
		if now.After(v.Expires) {
			delete(s.codes, k)
		}
	}
	for k, v := range s.pending {
		if now.After(v.Expires) {
			delete(s.pending, k)
		}
	}
}

var errTooManyClients = errors.New("too many registered clients")

func (s *oauthStore) addClient(c *oauthClient) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	if len(s.Clients) >= maxClients {
		s.pruneClientsLocked()
		if len(s.Clients) >= maxClients {
			return errTooManyClients
		}
	}
	s.Clients[c.ID] = c
	return s.saveLocked()
}

// pruneClientsLocked removes the oldest clients that hold no live token. Claude
// registers a fresh client on every connection, so these pile up.
func (s *oauthStore) pruneClientsLocked() {
	live := map[string]bool{}
	for _, a := range s.Access {
		live[a.ClientID] = true
	}
	for _, r := range s.Refresh {
		live[r.ClientID] = true
	}
	var idle []*oauthClient
	for _, c := range s.Clients {
		if !live[c.ID] {
			idle = append(idle, c)
		}
	}
	sort.Slice(idle, func(i, j int) bool { return idle[i].Created.Before(idle[j].Created) })
	for _, c := range idle[:len(idle)/2+len(idle)%2] {
		delete(s.Clients, c.ID)
	}
}

func (s *oauthStore) client(id string) *oauthClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Clients[id]
}

func (s *oauthStore) addPending(p *pendingAuth) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	id := randomToken(24)
	p.Expires = s.now().Add(pendingTTL)
	s.pending[id] = p
	return id
}

func (s *oauthStore) getPending(id string) *pendingAuth {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[id]
	if p == nil || s.now().After(p.Expires) {
		delete(s.pending, id)
		return nil
	}
	return p
}

func (s *oauthStore) dropPending(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, id)
}

func (s *oauthStore) addCode(c *authCode) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	code := randomToken(32)
	c.Expires = s.now().Add(codeTTL)
	s.codes[hashOf(code)] = c
	return code
}

// takeCode removes and returns a code. A code works once, whatever happens next.
func (s *oauthStore) takeCode(code string) *authCode {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := hashOf(code)
	c := s.codes[h]
	delete(s.codes, h)
	if c == nil || s.now().After(c.Expires) {
		return nil
	}
	return c
}

// issueLocked mints an access and refresh token in a family.
func (s *oauthStore) issueLocked(clientID, resource, family string) (access, refresh string) {
	access, refresh = randomToken(32), randomToken(32)
	now := s.now()
	s.Access[hashOf(access)] = &accessToken{ClientID: clientID, Resource: resource, Family: family, Expires: now.Add(accessTTL)}
	s.Refresh[hashOf(refresh)] = &refreshToken{ClientID: clientID, Resource: resource, Family: family, Expires: now.Add(refreshTTL)}
	return
}

func (s *oauthStore) issue(clientID, resource string) (access, refresh string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	access, refresh = s.issueLocked(clientID, resource, randomToken(12))
	return access, refresh, s.saveLocked()
}

var errReuse = errors.New("refresh token reuse detected; the grant was revoked")

// rotate exchanges a refresh token for a new pair. Presenting a token that was
// already rotated means two parties hold it, so the whole family is revoked.
func (s *oauthStore) rotate(clientID, token string) (access, refresh, resource string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	h := hashOf(token)
	rt := s.Refresh[h]
	if rt == nil {
		if m, ok := s.Rotated[h]; ok {
			s.revokeFamilyLocked(m.Family)
			_ = s.saveLocked()
			return "", "", "", errReuse
		}
		return "", "", "", errors.New("unknown or expired refresh token")
	}
	if rt.ClientID != clientID {
		return "", "", "", errors.New("refresh token was issued to another client")
	}
	delete(s.Refresh, h)
	s.Rotated[h] = rotatedMark{Family: rt.Family, Expires: s.now().Add(rotatedKeep)}
	access, refresh = s.issueLocked(clientID, rt.Resource, rt.Family)
	return access, refresh, rt.Resource, s.saveLocked()
}

func (s *oauthStore) revokeFamilyLocked(family string) {
	for k, v := range s.Access {
		if v.Family == family {
			delete(s.Access, k)
		}
	}
	for k, v := range s.Refresh {
		if v.Family == family {
			delete(s.Refresh, k)
		}
	}
}

// validAccess reports whether a bearer token is a live access token for this resource.
func (s *oauthStore) validAccess(token, resource string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.Access[hashOf(token)]
	if a == nil || s.now().After(a.Expires) {
		return false
	}
	return a.Resource == resource
}
