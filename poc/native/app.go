package main

import (
	"context"
	"time"
)

type app struct {
	sendEnabled, filesTab                 bool
	conversation                          string
	exchange                              *exchangeClient
	messages                              map[string]*messagePane
	files                                 map[string]*filePane
	updated                               time.Time
	ja, demo, loaded, busy, failed, about bool
	query, selected                       string
	state                                 snapshot
	ctx                                   context.Context
	load                                  loader
	results                               chan result
}

func (a *app) t(en, ja string) string {
	if a.ja {
		return ja
	}
	return en
}
func (a *app) refresh(wake func()) {
	if a.demo || a.busy || a.load == nil {
		return
	}
	a.busy = true
	a.failed = false
	go func() {
		s, err := a.load(a.ctx)
		select {
		case a.results <- result{s, err}:
			wake()
		case <-a.ctx.Done():
		}
	}()
}
func (a *app) drain() {
	select {
	case r := <-a.results:
		a.busy = false
		a.failed = r.err != nil
		a.loaded = r.err == nil
		if r.err == nil {
			a.state = r.state
			a.updated = time.Now()
		} else {
			a.state = snapshot{}
			a.selected = ""
		}
	default:
	}
}

func (a *app) toggleLocale() { a.ja = !a.ja }
func (a *app) toggleAbout()  { a.about = !a.about }
func (a *app) togglePeer(id string) {
	if a.selected == id {
		a.selected = ""
	} else {
		a.selected = id
	}
}
func (a *app) openConversation()             { a.conversation = a.selected }
func (a *app) closeConversation()            { a.conversation = "" }
func (a *app) selectConversationTab(tab int) { a.filesTab = tab == 1 }

// Conversation state lives with the app, never with a build-pass element.
func (a *app) prepareConversation() {
	if a.messages == nil {
		a.messages = map[string]*messagePane{}
	}
	if a.files == nil {
		a.files = map[string]*filePane{}
	}
	if a.messages[a.conversation] == nil {
		a.messages[a.conversation] = &messagePane{}
	}
	if a.files[a.conversation] == nil {
		a.files[a.conversation] = &filePane{}
	}
}
