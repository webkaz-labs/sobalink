package main

import (
	"context"
	"reflect"
)

// filePane owns drafts across flyout dismissal. Workers only publish events;
// rendering drains them on the UI thread. An uncertain mutation is never retried.
type filePane struct {
	generation                 uint64
	announced                  string
	busy                       bool
	events                     chan filePaneEvent
	review                     fileReview
	receipt                    fileReceipt
	actionID                   string
	process, peer, preparation string
	uncertain                  bool
	notice                     string
	incoming                   *exchangeTransfer
	destination                string
	decline                    bool
	acted                      map[string]bool
}
type filePaneEvent struct {
	generation  uint64
	kind        string
	review      fileReview
	receipt     fileReceipt
	state       fileDispatchState
	destination string
	err         error
}

func (p *filePane) launch(ctx context.Context, wake func(), fn func() filePaneEvent) {
	if p.busy || ctx == nil || ctx.Err() != nil {
		return
	}
	if p.events == nil {
		p.events = make(chan filePaneEvent, 1)
	}
	p.busy = true
	p.generation++
	generation, events := p.generation, p.events
	go func() {
		e := fn()
		e.generation = generation
		// Single flight guarantees room for completion, even after cancellation.
		events <- e
		if ctx.Err() == nil && wake != nil {
			wake()
		}
	}()
}
func (p *filePane) drain() {
	if p.events == nil {
		return
	}
	select {
	case e := <-p.events:
		if e.generation != p.generation {
			return
		}
		p.busy = false
		if e.err != nil {
			p.notice = "failed"
			if e.kind != "pick" && e.kind != "destination" && e.kind != "status" {
				p.uncertain = true
				p.notice = "uncertain"
			}
			return
		}
		switch e.kind {
		case "prepare":
			p.review = e.review
			p.notice = "review"
		case "pick":
			p.notice = ""
		case "commit":
			p.receipt = e.receipt
			p.review = fileReview{}
			p.notice = "submitted"
		case "discard":
			p.review = fileReview{}
			p.preparation = ""
			p.notice = "discarded"
		case "destination":
			p.destination = e.destination
		case "incoming":
			p.incoming = nil
			p.destination = ""
			p.notice = "submitted"
		case "status":
			if e.state.Status == "committed" {
				p.receipt = fileReceipt{Version: e.state.Version, ProcessID: e.state.ProcessID, PreparationID: e.state.PreparationID, DispatchID: e.state.DispatchID, Children: append([]fileChild(nil), e.state.Children...), Status: e.state.Status}
				p.review = fileReview{}
				p.notice = "submitted"
			} else {
				p.notice = "prepared"
			}
		}
	default:
	}
}
func pendingOffer(t exchangeTransfer) bool {
	return t.ID != "" && t.Direction == "incoming" && t.Status == "awaiting-acceptance"
}
func sameOffer(a, b exchangeTransfer) bool {
	return pendingOffer(a) && a.ID == b.ID && a.PeerID == b.PeerID && pendingOffer(b) && a.TotalBytes == b.TotalBytes && reflect.DeepEqual(a.Entries, b.Entries)
}

// fileActions captures the current UI selection. Operation guards are repeated
// here, not entrusted to disabled buttons, so callbacks cannot bypass consent.
type fileActions struct {
	ctx           context.Context
	wake          func()
	demo, enabled bool
	client        *exchangeClient
	process, peer string
	transfers     []exchangeTransfer
	pick          func(directory bool) ([]string, error)
}

func (a fileActions) allowed() bool {
	return a.enabled && a.peer != "" && (a.demo || a.client != nil && a.process != "")
}
func (p *filePane) ready(a fileActions) bool { return a.ctx != nil && a.ctx.Err() == nil && !p.busy }
func (p *filePane) canChoose(a fileActions) bool {
	return p.ready(a) && a.allowed() && p.review.PreparationID == "" && !p.uncertain && p.incoming == nil
}
func (p *filePane) chooseFiles(a fileActions) {
	if !p.canChoose(a) {
		return
	}
	p.process, p.peer, p.preparation = a.process, a.peer, newExchangeRequestID()
	proc, peer, prep := p.process, p.peer, p.preparation
	p.actionID = prep
	p.launch(a.ctx, a.wake, func() filePaneEvent {
		if a.demo {
			return filePaneEvent{kind: "prepare", review: fileReview{Version: 1, ProcessID: proc, PreparationID: prep, ReviewRevision: "demo-review", PeerIDs: []string{peer}, Entries: []fileEntry{{ID: "demo-file", Path: "sample.txt", Kind: "file", Size: 256}}, TotalBytes: 256, Status: "prepared"}}
		}
		paths, err := a.pick(false)
		if err != nil || len(paths) == 0 {
			return filePaneEvent{kind: "pick", err: err}
		}
		review, err := a.client.PrepareFiles(a.ctx, prep, proc, []string{peer}, paths)
		return filePaneEvent{kind: "prepare", review: review, err: err}
	})
}
func (p *filePane) canCommit(a fileActions) bool {
	return p.ready(a) && a.allowed() && !p.uncertain && p.review.PreparationID != "" && a.peer == p.peer && a.process == p.process
}
func (p *filePane) commitFiles(a fileActions) {
	if !p.canCommit(a) {
		return
	}
	review := p.review
	p.actionID = newExchangeRequestID()
	id := p.actionID
	p.launch(a.ctx, a.wake, func() filePaneEvent {
		if a.demo {
			return filePaneEvent{kind: "commit", receipt: fileReceipt{Version: 1, ProcessID: review.ProcessID, PreparationID: review.PreparationID, DispatchID: review.PreparationID, Children: []fileChild{{PeerID: a.peer, TransferID: "demo-transfer"}}, Status: "committed"}}
		}
		receipt, err := a.client.CommitFiles(a.ctx, id, review, true)
		return filePaneEvent{kind: "commit", receipt: receipt, err: err}
	})
}
func (p *filePane) canDiscard(a fileActions) bool {
	return p.ready(a) && a.enabled && !p.uncertain && p.review.PreparationID != "" && (a.demo || a.client != nil)
}
func (p *filePane) discardFiles(a fileActions) {
	if !p.canDiscard(a) {
		return
	}
	proc, prep := p.process, p.preparation
	p.actionID = newExchangeRequestID()
	id := p.actionID
	p.launch(a.ctx, a.wake, func() filePaneEvent {
		if a.demo {
			return filePaneEvent{kind: "discard"}
		}
		return filePaneEvent{kind: "discard", err: a.client.DiscardFiles(a.ctx, id, proc, prep)}
	})
}
func (p *filePane) canCheckStatus(a fileActions) bool {
	return p.ready(a) && p.uncertain && p.preparation != "" && (a.demo || a.client != nil)
}
func (p *filePane) checkStatus(a fileActions) {
	if !p.canCheckStatus(a) {
		return
	}
	proc, prep := p.process, p.preparation
	p.launch(a.ctx, a.wake, func() filePaneEvent {
		if a.demo {
			return filePaneEvent{kind: "status", state: fileDispatchState{fileReview: fileReview{Status: "prepared"}}}
		}
		state, err := a.client.FileStatus(a.ctx, newExchangeRequestID(), proc, prep)
		return filePaneEvent{kind: "status", state: state, err: err}
	})
}
func (p *filePane) offerCurrent(a fileActions, offer exchangeTransfer) bool {
	if offer.PeerID != a.peer {
		return false
	}
	for _, v := range a.transfers {
		if sameOffer(offer, v) {
			return true
		}
	}
	return false
}
func (p *filePane) canReviewIncoming(a fileActions, offer exchangeTransfer) bool {
	return p.ready(a) && a.allowed() && !p.uncertain && p.review.PreparationID == "" && !p.acted[offer.ID] && p.offerCurrent(a, offer)
}
func (p *filePane) reviewIncoming(a fileActions, offer exchangeTransfer, decline bool) {
	if !p.canReviewIncoming(a, offer) {
		return
	}
	copy := offer
	copy.Entries = append([]exchangeTransferEntry(nil), offer.Entries...)
	p.incoming, p.preparation, p.destination, p.decline = &copy, "", "", decline
}
func (p *filePane) canChooseDestination(a fileActions) bool {
	return p.ready(a) && a.allowed() && !p.uncertain && p.incoming != nil && !p.decline && !p.acted[p.incoming.ID] && p.offerCurrent(a, *p.incoming)
}
func (p *filePane) chooseDestination(a fileActions) {
	if !p.canChooseDestination(a) {
		return
	}
	p.launch(a.ctx, a.wake, func() filePaneEvent {
		if a.demo {
			return filePaneEvent{kind: "destination", destination: "Demo Downloads"}
		}
		paths, err := a.pick(true)
		dest := ""
		if len(paths) > 0 {
			dest = paths[0]
		}
		return filePaneEvent{kind: "destination", destination: dest, err: err}
	})
}
func (p *filePane) canConfirmIncoming(a fileActions) bool {
	return p.ready(a) && a.allowed() && !p.uncertain && p.incoming != nil && !p.acted[p.incoming.ID] && p.offerCurrent(a, *p.incoming) && (p.decline || p.destination != "")
}
func (p *filePane) confirmIncoming(a fileActions) {
	if !p.canConfirmIncoming(a) {
		return
	}
	offer, dest, decline := *p.incoming, p.destination, p.decline
	if p.acted == nil {
		p.acted = map[string]bool{}
	}
	p.acted[offer.ID] = true
	p.actionID = newExchangeRequestID()
	id := p.actionID
	p.launch(a.ctx, a.wake, func() filePaneEvent {
		if a.demo {
			return filePaneEvent{kind: "incoming"}
		}
		var err error
		if decline {
			err = a.client.DeclineFiles(a.ctx, id, offer.ID, true)
		} else {
			err = a.client.AcceptFiles(a.ctx, id, offer.ID, dest, true)
		}
		return filePaneEvent{kind: "incoming", err: err}
	})
}
func (p *filePane) backIncoming() {
	if p.busy {
		return
	}
	p.incoming, p.destination = nil, ""
}

func (p *filePane) takeAnnouncement() bool {
	status := p.notice
	if p.busy {
		status = "busy"
	}
	if status == p.announced {
		return false
	}
	p.announced = status
	return status != ""
}
