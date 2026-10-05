package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// LoginView is returned only by explicit local login operations. It must never
// become part of ordinary status, discovery, history or logging.
type LoginView struct {
	State   string   `json:"state"`
	AuthURL string   `json:"authUrl,omitempty"`
	QR      [][]bool `json:"qr,omitempty"`
}

func ValidAuthURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Host != "login.tailscale.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(u.Path, "/a/") {
		return false
	}
	token := strings.TrimPrefix(u.Path, "/a/")
	if token == "" {
		return false
	}
	for _, r := range token {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (c *Core) loginCommand(ctx context.Context, start bool, raw json.RawMessage) (LoginView, error) {
	var input struct {
		QR      bool `json:"qr"`
		Refresh bool `json:"refresh"`
	}
	if err := decodePayload(raw, &input); err != nil {
		return LoginView{}, err
	}
	if !start && input.Refresh {
		return LoginView{}, errors.New("refresh requires an explicit login request")
	}
	mode := c.profileCopy().Settings.Network
	loginNode := c.nodeCopy()
	if mixed, ok := loginNode.(*mixedBackend); ok {
		loginNode = mixed.nodes["tailnet"]
	}
	if (mode != "tailnet" && mode != "mixed") || loginNode == nil {
		return LoginView{}, &localCommandError{"login_network_required", "activate the Tailnet network before signing in"}
	}
	state, err := loginNode.State(ctx)
	if err != nil {
		return LoginView{}, err
	}
	if state.Snapshot.Running {
		return LoginView{State: "connected"}, nil
	}
	if state.Backend == "NeedsMachineAuth" {
		return LoginView{State: "approval-required"}, nil
	}
	if start && (state.AuthURL == "" || input.Refresh) {
		if err := loginNode.Login(ctx); err != nil {
			return LoginView{}, &localCommandError{"login_request_failed", "could not request interactive login; check the network and retry"}
		}
		state, err = loginNode.State(ctx)
		if err != nil {
			return LoginView{}, err
		}
	}
	view := LoginView{State: "waiting"}
	if state.Snapshot.Running {
		view.State = "connected"
	} else if state.Backend == "NeedsMachineAuth" {
		view.State = "approval-required"
	} else if state.AuthURL != "" {
		if !ValidAuthURL(state.AuthURL) {
			return LoginView{}, &localCommandError{"login_url_invalid", "unexpected sign-in address; restart the official sign-in flow"}
		}
		view.AuthURL = state.AuthURL
		if input.QR {
			code, err := qrcode.New(view.AuthURL, qrcode.Medium)
			if err != nil {
				return LoginView{}, &localCommandError{"login_qr_failed", "QR generation failed; use the private sign-in link"}
			}
			view.QR = code.Bitmap()
		}
	}
	return view, nil
}
