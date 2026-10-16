package webui

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestUILaunchReusesSessionWithoutExtendingAuthority(t *testing.T) {
	s, _ := testServer(t)
	cookie, csrf := signIn(t, s)
	s.mu.Lock()
	previous := s.sessions[cookie.Value]
	previous.expires = time.Now().Add(time.Second)
	s.sessions[cookie.Value] = previous
	for i := range 7 {
		s.sessions["other-"+strconv.Itoa(i)] = session{csrf: "other", expires: time.Now().Add(time.Hour)}
	}
	s.mu.Unlock()
	code, err := s.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	bad := serve(s, request(s, "POST", "/api/session", `{"code":"wrong"}`, cookie, ""))
	if bad.Code != http.StatusUnauthorized {
		t.Fatal("cookie bypassed code verification")
	}
	w := serve(s, request(s, "POST", "/api/session", `{"code":"`+code+`"}`, cookie, ""))
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 0 {
		t.Fatal("launch replaced session or failed at capacity")
	}
	var result struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.CSRF != csrf {
		t.Fatal("CSRF changed")
	}
	s.mu.Lock()
	current, count, remaining := s.sessions[cookie.Value], len(s.sessions), s.code
	s.mu.Unlock()
	if current != previous || count != 8 || remaining != "" {
		t.Fatal("launch extended, duplicated, or left capability live")
	}
	if replay := serve(s, request(s, "POST", "/api/session", `{"code":"`+code+`"}`, cookie, "")); replay.Code != http.StatusUnauthorized {
		t.Fatal("replay accepted")
	}
}

func TestUILaunchExpiredCookieDoesNotRestoreOldSession(t *testing.T) {
	s, _ := testServer(t)
	cookie, csrf := signIn(t, s)
	s.mu.Lock()
	s.sessions[cookie.Value] = session{csrf: csrf, expires: time.Now().Add(-time.Second)}
	s.mu.Unlock()
	code, err := s.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	w := serve(s, request(s, "POST", "/api/session", `{"code":"`+code+`"}`, cookie, ""))
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].Value == cookie.Value {
		t.Fatal("expired session was reused")
	}
	s.mu.Lock()
	_, oldExists := s.sessions[cookie.Value]
	s.mu.Unlock()
	if oldExists {
		t.Fatal("expired cookie remained")
	}
}

func TestUILaunchConcurrentReuseConsumesOnce(t *testing.T) {
	s, _ := testServer(t)
	cookie, csrf := signIn(t, s)
	s.mu.Lock()
	previous := s.sessions[cookie.Value]
	s.mu.Unlock()
	code, err := s.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan int, 8)
	for range 8 {
		go func() {
			w := serve(s, request(s, "POST", "/api/session", `{"code":"`+code+`"}`, cookie, ""))
			if len(w.Result().Cookies()) != 0 {
				results <- -1
				return
			}
			results <- w.Code
		}()
	}
	successes := 0
	for range 8 {
		status := <-results
		if status == http.StatusOK {
			successes++
		} else if status != http.StatusUnauthorized {
			t.Fatalf("unexpected reuse status %d", status)
		}
	}
	if successes != 1 {
		t.Fatal("concurrent capability was not consumed exactly once")
	}
	s.mu.Lock()
	current, count, remaining := s.sessions[cookie.Value], len(s.sessions), s.code
	s.mu.Unlock()
	if current != previous || current.csrf != csrf || count != 1 || remaining != "" {
		t.Fatal("concurrent reuse changed session authority")
	}
}
