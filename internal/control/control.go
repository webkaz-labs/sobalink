// Package control is a bounded JSON protocol over user-private local IPC.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

type Request struct {
	Command string `json:"command"`
}
type Response struct {
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}
type Handler func(context.Context, string) (any, error)
type Server struct {
	ln        net.Listener
	wg        sync.WaitGroup
	cancel    context.CancelFunc
	mu        sync.Mutex
	conns     map[net.Conn]bool
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func Serve(parent context.Context, dir string, h Handler) (*Server, error) {
	ln, e := listen(dir)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Server{ln: ln, cancel: cancel, conns: make(map[net.Conn]bool)}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		slots := make(chan struct{}, 16)
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				c.Close()
				continue
			}
			s.mu.Lock()
			if s.closed || ctx.Err() != nil {
				s.mu.Unlock()
				c.Close()
				<-slots
				return
			}
			s.conns[c] = true
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer func() { <-slots; s.mu.Lock(); delete(s.conns, c); s.mu.Unlock() }()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				var r Request
				d := json.NewDecoder(io.LimitReader(c, 4096))
				d.DisallowUnknownFields()
				if e := d.Decode(&r); e != nil {
					return
				}
				callCtx, stop := context.WithTimeout(ctx, 15*time.Second)
				defer stop()
				v, e := h(callCtx, r.Command)
				resp := Response{}
				if e != nil {
					resp.Error = e.Error()
				} else {
					resp.Data, e = json.Marshal(v)
					if e != nil {
						resp.Error = "could not encode response"
					}
				}
				_ = json.NewEncoder(c).Encode(resp)
			}()
		}
	}()
	return s, nil
}
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.closeErr = s.ln.Close()
		// Let the stop/logout response drain before canceling remaining handlers.
		joined := make(chan struct{})
		go func() { s.wg.Wait(); close(joined) }()
		select {
		case <-joined:
		case <-time.After(200 * time.Millisecond):
		}
		s.cancel()
		s.mu.Lock()
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
		<-joined
	})
	return s.closeErr
}

func Call(ctx context.Context, dir, command string, v any) error {
	c, e := dial(ctx, dir)
	if e != nil {
		return e
	}
	defer c.Close()
	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetDeadline(deadline)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-done:
		}
	}()
	if e = json.NewEncoder(c).Encode(Request{command}); e != nil {
		return e
	}
	var r Response
	if e = json.NewDecoder(io.LimitReader(c, 64<<10)).Decode(&r); e != nil {
		return e
	}
	if r.Error != "" {
		return errors.New(r.Error)
	}
	if v != nil {
		return json.Unmarshal(r.Data, v)
	}
	return nil
}
