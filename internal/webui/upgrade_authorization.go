package webui

import (
	"encoding/json"
	"errors"
	"time"
)

type upgradeAuthorization struct {
	session, csrf, payload string
	expires                time.Time
	timer                  *time.Timer
}

func canonicalUpgradeRequest(raw json.RawMessage) (string, error) {
	var value any
	if len(raw) > 2048 || json.Unmarshal(raw, &value) != nil {
		return "", errors.New("invalid upgrade request")
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func (s *Server) issueUpgradeAuthorization(sessionID string, current session, raw json.RawMessage) (string, error) {
	payload, err := canonicalUpgradeRequest(raw)
	if err != nil {
		return "", err
	}
	var request struct {
		Intent struct {
			Deadline string `json:"deadline"`
		} `json:"intent"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return "", errors.New("invalid upgrade deadline")
	}
	deadline, err := time.Parse(time.RFC3339Nano, request.Intent.Deadline)
	if err != nil {
		return "", errors.New("invalid upgrade deadline")
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	actual, ok := s.sessions[sessionID]
	if !ok || actual != current || !now.Before(actual.expires) {
		return "", errors.New("session expired")
	}
	if s.handoffs == nil {
		s.handoffs = make(map[string]upgradeAuthorization)
	}
	for id, grant := range s.handoffs {
		if !now.Before(grant.expires) {
			if grant.timer != nil {
				grant.timer.Stop()
			}
			delete(s.handoffs, id)
		}
	}
	if len(s.handoffs) >= 16 {
		return "", errors.New("handoff limit")
	}
	if !deadline.After(now) || deadline.Sub(now) > 5*time.Minute {
		return "", errors.New("expired upgrade deadline")
	}
	until := deadline
	if current.expires.Before(until) {
		until = current.expires
	}
	timer := time.AfterFunc(time.Until(until), func() { s.discardUpgradeAuthorization(token) })
	s.handoffs[token] = upgradeAuthorization{session: sessionID, csrf: current.csrf, payload: payload, expires: until, timer: timer}
	return token, nil
}

func (s *Server) discardUpgradeAuthorization(token string) {
	s.mu.Lock()
	if grant, ok := s.handoffs[token]; ok && grant.timer != nil {
		grant.timer.Stop()
	}
	delete(s.handoffs, token)
	s.mu.Unlock()
}

// CheckUpgradeAuthorization is used only by the protected local lifecycle
// owner. consume marks the linearization point authorizing old-process stop.
// Before that point logout/session expiry invalidates the delegation; after it
// only the exact bounded restart intent continues, without a browser session.
func (s *Server) CheckUpgradeAuthorization(token string, raw json.RawMessage, consume bool) error {
	payload, err := canonicalUpgradeRequest(raw)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.handoffs[token]
	current, live := s.sessions[grant.session]
	now := time.Now()
	if !ok || !live || current.csrf != grant.csrf || !now.Before(current.expires) || !now.Before(grant.expires) || payload != grant.payload {
		return errors.New("Web restart authorization expired or changed")
	}
	if consume {
		if grant.timer != nil {
			grant.timer.Stop()
		}
		delete(s.handoffs, token)
	}
	return nil
}
