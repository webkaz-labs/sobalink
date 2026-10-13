package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/webui"
)

func nearbyTestConnected(joined bool) nearbySnapshot {
	s := nearbyTestPlanned()
	s.State = "connected"
	s.Connected = true
	s.Paired = true
	s.LocalApproved = true
	s.RemoteApproved = true
	s.NextAction = "done"
	if joined {
		s.NextAction = "start_new_setup"
	}
	return s
}
func TestNearbyRepeatSetupDeclineOrEOFLeavesConnectionAlone(t *testing.T) {
	for _, joined := range []bool{false, true} {
		for _, input := range []string{"", "n\n", "q\n"} {
			for _, locale := range []string{"en", "ja"} {
				calls := []webui.Command{}
				s := nearbyTestConnected(joined)
				var out bytes.Buffer
				if err := runWith(context.Background(), []string{"--locale", locale, "--state-dir", "/synthetic/profile", "nearby", "--interactive"}, &out, strings.NewReader(input), nearbyFixtureClient(t, &calls, nil, s)); err != nil {
					t.Fatal(err)
				}
				if len(calls) != 1 || calls[0].Name != "setup.status" {
					t.Fatalf("decline/EOF mutated connection: %v", calls)
				}
				expected := "Connect another device?"
				if locale == "ja" {
					expected = "別の端末を接続しますか"
				}
				if !strings.Contains(out.String(), expected) {
					t.Fatal(out.String())
				}
			}
		}
	}
}
func TestNearbyRepeatSetupWaitsForJoinedMarkerThenRequiresFreshConsent(t *testing.T) {
	for _, alreadyJoined := range []bool{false, true} {
		original := nearbyTestConnected(alreadyJoined)
		joined := original
		joined.NextAction = "start_new_setup"
		pending := original
		pending.State = "cancelling"
		pending.Connected = false
		pending.NextAction = "wait_for_cleanup"
		fresh := nearbyTestSnapshot("discovering")
		fresh.SessionID = strings.Repeat("9a", 32)
		contact := nearbyTestContact(fresh.SessionID, "Device C", "192.168.70.10:54546")
		fresh.Contact = &contact
		fresh.ContactText = nearbyTestEncode(contact)
		calls := []webui.Command{}
		statusPolls := 0
		startSeen := false
		client := func(_ context.Context, dir, raw string, target any) error {
			if dir != "/synthetic/profile" {
				t.Fatal("profile changed")
			}
			var command webui.Command
			json.Unmarshal([]byte(raw), &command)
			calls = append(calls, command)
			var fields map[string]any
			json.Unmarshal(command.Payload, &fields)
			var response any
			switch command.Name {
			case "setup.status":
				if len(calls) == 1 {
					response = original
					break
				}
				if fields["sessionId"] != original.SessionID {
					t.Fatal("lost original receipt locator", fields)
				}
				statusPolls++
				if statusPolls == 1 {
					response = nearbyTestConnected(false)
				} else {
					response = joined
				}
			case "setup.cancel":
				if alreadyJoined || fields["sessionId"] != original.SessionID {
					t.Fatal("unneeded or wrong receipt cancel")
				}
				response = pending
			case "setup.scopes":
				if !alreadyJoined && statusPolls < 2 {
					t.Fatal("started new review before owner joined")
				}
				response = nearbyScopes{"sobalink-setup-scopes", 1, []nearbyScope{nearbyTestScope()}}
			case "setup.start":
				var input nearbyStartInput
				json.Unmarshal(command.Payload, &input)
				if input.Alias != "Device C" || !input.Consent || !input.AllowHost || input.ScopeID != nearbyTestScope().ScopeID {
					t.Fatal("fresh consent missing", input)
				}
				for _, name := range []string{"sessionId", "revision", "fingerprint", "verified"} {
					if fields[name] != nil {
						t.Fatal("old approval reused", fields)
					}
				}
				startSeen = true
				response = fresh
			default:
				t.Fatalf("unexpected action: %s", command.Name)
			}
			data, _ := json.Marshal(response)
			return json.Unmarshal(data, target)
		}
		var out bytes.Buffer
		if err := runWith(context.Background(), []string{"--state-dir", "/synthetic/profile", "nearby", "--interactive"}, &out, strings.NewReader("y\nDevice C\ny\ny\nx\n"), client); err != nil {
			t.Fatal(err)
		}
		if !startSeen {
			t.Fatal("no deliberate fresh start")
		}
		ids := map[string]bool{}
		for _, command := range calls {
			if command.RequestID == "" || ids[command.RequestID] {
				t.Fatal("request identity reused")
			}
			ids[command.RequestID] = true
		}
		if !alreadyJoined && !strings.Contains(out.String(), "no new setup has started") {
			t.Fatal("missing cleanup wait disclosure")
		}
	}
}
func TestNearbyRepeatSetupNewReviewDeclineDoesNotStart(t *testing.T) {
	for _, input := range []string{"y\n", "y\nDevice C\ny\nn\n"} {
		s := nearbyTestConnected(true)
		calls := []webui.Command{}
		var out bytes.Buffer
		if err := runWith(context.Background(), []string{"nearby", "--interactive"}, &out, strings.NewReader(input), nearbyFixtureClient(t, &calls, []nearbyScope{nearbyTestScope()}, s)); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 2 || calls[0].Name != "setup.status" || calls[1].Name != "setup.scopes" {
			t.Fatal("new start without fresh consent", calls)
		}
	}
}
func TestNearbyRepeatSetupLostCancelOrStatusKeepsOriginalLocator(t *testing.T) {
	for _, failAction := range []string{"setup.cancel", "setup.status"} {
		s := nearbyTestConnected(false)
		calls := []webui.Command{}
		var out bytes.Buffer
		client := func(_ context.Context, _ string, raw string, target any) error {
			var command webui.Command
			json.Unmarshal([]byte(raw), &command)
			calls = append(calls, command)
			if len(calls) > 1 && command.Name == failAction {
				return errors.New("synthetic unreadable response")
			}
			response := s
			if command.Name == "setup.cancel" {
				response.State = "cancelling"
				response.Connected = false
				response.NextAction = "wait_for_cleanup"
			}
			data, _ := json.Marshal(response)
			return json.Unmarshal(data, target)
		}
		err := runWith(context.Background(), []string{"--state-dir", "/synthetic/profile", "nearby", "--interactive"}, &out, strings.NewReader("y\n"), client)
		if nearbyErrorCode(t, err) != "setup_control_unavailable" {
			t.Fatal(err)
		}
		want := 2
		if failAction == "setup.status" {
			want = 3
		}
		if len(calls) != want {
			t.Fatal("uncertain action retried", calls)
		}
		if !strings.Contains(out.String(), "soba --state-dir /synthetic/profile nearby status --session "+s.SessionID) {
			t.Fatal("lost original locator", out.String())
		}
	}
}
func TestNearbyRepeatSetupLostFreshStartQueriesCurrentSession(t *testing.T) {
	s := nearbyTestConnected(true)
	calls := []webui.Command{}
	var out bytes.Buffer
	client := func(_ context.Context, _ string, raw string, target any) error {
		var command webui.Command
		json.Unmarshal([]byte(raw), &command)
		calls = append(calls, command)
		var response any = s
		if command.Name == "setup.scopes" {
			response = nearbyScopes{"sobalink-setup-scopes", 1, []nearbyScope{nearbyTestScope()}}
		}
		if command.Name == "setup.start" {
			out.Reset()
			return errors.New("lost fresh start response")
		}
		data, _ := json.Marshal(response)
		return json.Unmarshal(data, target)
	}
	err := runWith(context.Background(), []string{"--state-dir", "/synthetic/profile", "nearby", "--interactive"}, &out, strings.NewReader("y\nDevice C\ny\ny\n"), client)
	if nearbyErrorCode(t, err) != "setup_control_unavailable" || len(calls) != 3 {
		t.Fatal(err, calls)
	}
	if !strings.Contains(out.String(), "soba --state-dir /synthetic/profile nearby status\n") || strings.Contains(out.String(), "--session") {
		t.Fatal("fresh-start uncertainty used old locator", out.String())
	}
}
func TestNearbyRepeatSetupRecoveryOrInterruptCannotStart(t *testing.T) {
	for _, state := range []string{"recovery_required", "failed", "cancelled"} {
		s := nearbyTestConnected(false)
		s.State = state
		s.Connected = false
		c := nearbyCLI{ctx: context.Background(), out: io.Discard, client: nearbyNoCalls(t)}
		if _, err := c.awaitReceiptJoined(s, time.Nanosecond); nearbyErrorCode(t, err) != "setup_state_changed" {
			t.Fatal(err)
		}
	}
	s := nearbyTestConnected(false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := nearbyCLI{ctx: ctx, out: io.Discard, client: nearbyNoCalls(t)}
	got, err := c.awaitReceiptJoined(s, time.Hour)
	if !errors.Is(err, context.Canceled) || got.SessionID != s.SessionID {
		t.Fatal(err, got)
	}
}
func TestNearbyRepeatSetupScriptGuidanceIsLocalizedAndProfileBound(t *testing.T) {
	for _, joined := range []bool{false, true} {
		for _, ja := range []bool{false, true} {
			var out bytes.Buffer
			s := nearbyTestConnected(joined)
			if err := nearbyWriteHuman(&out, "/synthetic/profile", ja, s); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "soba --state-dir /synthetic/profile nearby --interactive") || !strings.Contains(out.String(), "start_new_setup") {
				t.Fatal(out.String())
			}
			if joined && !strings.Contains(out.String(), "soba --state-dir /synthetic/profile nearby start --alias NEW_ALIAS --scope SCOPE_ID --consent") {
				t.Fatal(out.String())
			}
			if !strings.Contains(out.String(), text(ja, "saved pair", "保存済み")) {
				t.Fatal(out.String())
			}
		}
	}
}
