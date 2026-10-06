package core

import (
	"encoding/json"
	"errors"
	"unicode/utf8"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/webkaz-labs/sobalink/internal/devicecard"
)

// DeviceCardExportView contains an explicit public allowlist, never a stored
// identity, PeerOffer (whose Address includes a PSK), or backend snapshot.
type DeviceCardExportView struct {
	Text string `json:"card"`
	devicecard.Card
	Verification string   `json:"verification"`
	Freshness    string   `json:"freshness"`
	QR           [][]bool `json:"qr,omitempty"`
}

func deviceCardError(err error) error {
	switch {
	case errors.Is(err, devicecard.ErrModeMismatch):
		return &localCommandError{"device_card_mode_mismatch", "device card is for a different network mode; select its mode explicitly"}
	case errors.Is(err, devicecard.ErrMode):
		return &localCommandError{"device_card_mode_required", "choose lan or direct-lan explicitly for this device card"}
	case errors.Is(err, devicecard.ErrTooLarge):
		return &localCommandError{"device_card_too_large", "device card exceeds its bounded text format"}
	default:
		return &localCommandError{"device_card_invalid", "invalid device card; use canonical card text and an explicit display name of 1–80 UTF-8 bytes without control or directional characters"}
	}
}

func (c *Core) deviceCardCommand(name string, raw json.RawMessage) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid command payload")
	}
	if name == "device-card.inspect" {
		var input struct {
			Card         string `json:"card"`
			ExpectedMode string `json:"expectedMode"`
		}
		if err := strictLANJSON(raw, &input); err != nil {
			return nil, errors.New("invalid command payload")
		}
		view, err := devicecard.Inspect(input.Card, input.ExpectedMode)
		if err != nil {
			return nil, deviceCardError(err)
		}
		return view, nil
	}
	var input struct {
		Mode                string `json:"mode"`
		Name                string `json:"name"`
		IncludeEndpointHint bool   `json:"includeEndpointHint,omitempty"`
		QR                  bool   `json:"qr,omitempty"`
	}
	if err := strictLANJSON(raw, &input); err != nil {
		return nil, errors.New("invalid command payload")
	}
	if !devicecard.ValidMode(input.Mode) {
		return nil, deviceCardError(devicecard.ErrMode)
	}
	card, err := c.publicDeviceCard(input.Mode, input.Name, input.IncludeEndpointHint)
	if err != nil {
		return nil, err
	}
	encoded, err := devicecard.Encode(card)
	if err != nil {
		return nil, deviceCardError(err)
	}
	view := DeviceCardExportView{Text: encoded, Card: card, Verification: "unverified", Freshness: "unknown"}
	if input.QR {
		// Plain card text, not an authentication URL. The existing encoder is
		// bounded by the card limit and adds no image-decoding dependency.
		code, err := qrcode.New(encoded, qrcode.Medium)
		if err != nil {
			return nil, &localCommandError{"device_card_qr_failed", "QR generation failed; export the device card as text"}
		}
		view.QR = code.Bitmap()
	}
	return view, nil
}

// publicDeviceCard performs no identity creation/repair, store writes, transport
// inspection or network access. Mixed profiles select the explicit component's
// store, never a mixed logical peer key. Caller-supplied alias is mandatory;
// configuration hostnames are deliberately not consulted.
func (c *Core) publicDeviceCard(mode, name string, includeEndpointHint bool) (devicecard.Card, error) {
	card := devicecard.Card{Version: 1, Mode: mode, Name: name}
	missing := func() (devicecard.Card, error) {
		return devicecard.Card{}, &localCommandError{"device_card_identity_required", "configure this network's identity explicitly before exporting its device card"}
	}
	recovery := func() (devicecard.Card, error) {
		return devicecard.Card{}, &localCommandError{"device_card_recovery_required", "saved network identity requires recovery; review protected state before exporting a device card"}
	}
	hintUnavailable := func() (devicecard.Card, error) {
		return devicecard.Card{}, &localCommandError{"device_card_hint_unavailable", "this network has no saved endpoint hint; export without the hint or configure its endpoint explicitly"}
	}
	c.mu.RLock()
	recoveryRequired := c.networkFatal != ""
	c.mu.RUnlock()
	if recoveryRequired {
		return recovery()
	}
	switch mode {
	case "lan":
		store := c.lanStoreCopy()
		if store == nil {
			return missing()
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if store.routeRecovery {
			return recovery()
		}
		if !supportedLANStateVersion(store.state.Version) || store.state.Identity.Validate() != nil {
			return recovery()
		}
		card.PublicKey = store.state.Identity.PublicKey()
		if includeEndpointHint {
			if store.state.Selection == nil || store.state.Selection.Address == "" || store.state.Selection.CertificateSHA256 == "" {
				return hintUnavailable()
			}
			card.Relay = &devicecard.RelayHint{Address: store.state.Selection.Address, CertificateSHA256: store.state.Selection.CertificateSHA256}
		}
	case "direct-lan":
		store := c.directLANStoreCopy()
		if store == nil {
			return missing()
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if store.recovery {
			return recovery()
		}
		if store.state.Version != directLANStateVersion || store.state.Identity.Validate() != nil {
			return recovery()
		}
		card.PublicKey = store.state.Identity.PublicKey()
		if includeEndpointHint {
			if store.state.Selection.Listen == "" {
				return hintUnavailable()
			}
			card.Endpoint = store.state.Selection.Listen
		}
	default:
		return devicecard.Card{}, deviceCardError(devicecard.ErrMode)
	}
	return card, nil
}
