package core

import (
	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Local request envelopes cover either decoded messages, saved definition
// bundles, or transfer path metadata. Response envelopes include escaped view
// fields and repeated stored paths, but never file contents.
func localLimitsFor(p capacity.Policy) (control.Limits, error) {
	p = policyOrDefault(p)
	text, err := messageframe.ForText(min(p.Number("logical", "messageBytes"), p.Number("resources", "messageTextBytes")))
	if err != nil {
		return control.Limits{}, err
	}
	payload := max(p.Number("resources", "profileBytes"), p.Number("resources", "transferManifestBytes"))
	command, err := messageframe.EncodedBytes(payload, 6, 64<<10)
	if err != nil {
		return control.Limits{}, err
	}
	command = max(command, text.CommandBytes)
	request, err := messageframe.EncodedBytes(command, 2, 64)
	if err != nil {
		return control.Limits{}, err
	}
	// Profiles can appear as definitions and live views. A snapshot may carry
	// several independent bounded pages, plus the retained message history.
	response, err := localResponseBytes(p, 2*p.Number("resources", "transferMetadataBytes"))
	if err != nil {
		return control.Limits{}, err
	}
	return control.Limits{CommandBytes: command, RequestBytes: max(request, text.ControlRequestBytes), ResponseBytes: response}, nil
}

func localResponseBytes(p capacity.Policy, metadata int64) (int64, error) {
	// Each finite capacity is at most a JSON-safe integer. Additions below
	// therefore fit int64; EncodedBytes also checks the final multiplication.
	units := p.Number("resources", "lanStateBytes") + p.Number("resources", "profileBytes") + p.Number("resources", "messageStorageBytes") + p.Number("resources", "pageBytes") + metadata
	return messageframe.EncodedBytes(units, 12, 64<<10)
}

// ReadLocalControlLimits reads only the separately bounded public capacity
// choices; it never loads credentials, invitations or a growing saved profile.
func ReadLocalControlLimits(dir string) (control.Limits, error) {
	p, err := readCapacityPolicy(dir)
	if err != nil {
		return control.Limits{}, err
	}
	return localLimitsFor(p)
}

// ReadCommandInputBytes selects the finite input allowance before reading any
// stdin/file payload. The invitation protocol keeps its separate small bound.
func ReadCommandInputBytes(dir, name string) (int64, error) {
	if name == "lan.join" {
		return 48 << 10, nil
	}
	p, err := readCapacityPolicy(dir)
	if err != nil {
		return 0, err
	}
	if name == "message.send" {
		limits, err := messageframe.ForText(min(p.Number("logical", "messageBytes"), p.Number("resources", "messageTextBytes")))
		return limits.CommandBytes, err
	}
	limits, err := localLimitsFor(p)
	return limits.CommandBytes, err
}

func (c *Core) CommandRequestBytes() int64 {
	limits, err := localLimitsFor(c.capacityPolicy())
	if err != nil {
		return 0
	}
	return limits.CommandBytes
}

func (c *Core) LocalControlLimits() control.Limits {
	p := c.capacityPolicy()
	limits, err := localLimitsFor(p)
	if err != nil {
		return control.Limits{}
	}
	// The selected budgets describe future admission. Add retained metadata so
	// a smaller new choice cannot hide already admitted transfers from clients.
	var retained int64
	if c.transfers != nil {
		retained = c.transfers.RetainedMetadataBytes()
	}
	c.mu.RLock()
	for _, batch := range c.outgoing {
		batch.mu.Lock()
		retained += transfer.ManifestMetadataBytes(batch.Manifest)
		batch.mu.Unlock()
	}
	c.mu.RUnlock()
	limits.ResponseBytes, err = localResponseBytes(p, 2*p.Number("resources", "transferMetadataBytes")+retained)
	if err != nil {
		return control.Limits{}
	}
	return limits
}
