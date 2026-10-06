// Package capacity separates user-selected limits from finite resource budgets.
package capacity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

const Version = 1
const MaxDurationSeconds = int64(math.MaxInt64) / int64(time.Second)
const MaxJSONInteger = int64(1<<53 - 1)

type Choice struct {
	Mode  string `json:"mode"`
	Value *int64 `json:"value,omitempty"`
}

func Default() Choice        { return Choice{Mode: "default"} }
func Limited(n int64) Choice { return Choice{Mode: "limited", Value: &n} }
func Unlimited() Choice      { return Choice{Mode: "unlimited"} }

// UnmarshalJSON rejects ambiguous nulls and duplicate or unknown fields.
func (c *Choice) UnmarshalJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return errors.New("limit choice must be an object")
	}
	var next Choice
	seen := map[string]bool{}
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return err
		}
		key, ok := t.(string)
		if !ok || seen[key] {
			return errors.New("duplicate limit field")
		}
		seen[key] = true
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("limit fields cannot be null")
		}
		switch key {
		case "mode":
			if err := json.Unmarshal(raw, &next.Mode); err != nil {
				return err
			}
		case "value":
			var n int64
			if err := json.Unmarshal(raw, &n); err != nil {
				return err
			}
			next.Value = &n
		default:
			return errors.New("unknown limit field")
		}
	}
	if _, err := d.Token(); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected limit data")
	}
	if err := next.Validate(true); err != nil {
		return err
	}
	*c = next
	return nil
}

func (c Choice) Validate(logical bool) error {
	switch c.Mode {
	case "default":
		if c.Value == nil {
			return nil
		}
	case "unlimited":
		if logical && c.Value == nil {
			return nil
		}
	case "limited":
		if c.Value != nil && *c.Value > 0 && *c.Value <= MaxJSONInteger {
			return nil
		}
	}
	return errors.New("choose default, a positive integer limit, or unlimited for a logical policy")
}

type Policy struct {
	Version   int               `json:"version"`
	Logical   map[string]Choice `json:"logical"`
	Resources map[string]Choice `json:"resources"`
}

func (p *Policy) UnmarshalJSON(raw []byte) error {
	fields, err := choiceObject(raw)
	if err != nil {
		return err
	}
	next := Defaults()
	version, ok := fields["version"]
	if !ok {
		return errors.New("capacity policy version is required")
	}
	if err := json.Unmarshal(version, &next.Version); err != nil {
		return err
	}
	for key, value := range fields {
		if key == "version" {
			continue
		}
		if key != "logical" && key != "resources" {
			return errors.New("unknown capacity policy section")
		}
		choices, err := choiceObject(value)
		if err != nil {
			return err
		}
		out := next.Logical
		if key == "resources" {
			out = next.Resources
		}
		for name, data := range choices {
			var choice Choice
			if err := json.Unmarshal(data, &choice); err != nil {
				return err
			}
			out[name] = choice
		}
	}
	if err := next.Validate(); err != nil {
		return err
	}
	*p = next
	return nil
}

func choiceObject(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("capacity policy object required")
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid policy field")
		}
		if _, ok := out[key]; ok {
			return nil, errors.New("duplicate capacity policy field")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errors.New("capacity policy fields cannot be null")
		}
		out[key] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing capacity policy data")
	}
	return out, nil
}

type Definition struct {
	Default int64  `json:"default"`
	Unit    string `json:"unit"`
}

// Catalog numbers are visible initial defaults, never maximum supported values.
var logicalDefinitions = map[string]Definition{
	"savedServices": {64, "entries"}, "trustedPeers": {128, "entries"}, "sharePeers": {32, "entries"},
	"rangePolicies": {64, "entries"}, "portIntervals": {256, "entries"},
	"groups": {32, "entries"}, "groupMembers": {64, "entries"},
	"batchEntries": {256, "entries"}, "fileBytes": {1 << 30, "bytes"}, "batchBytes": {1 << 30, "bytes"},
	"pathDepth": {16, "levels"}, "pathBytes": {4096, "bytes"},
	"messageBytes": {16 << 10, "bytes"}, "messageHistoryEntries": {128, "entries"},
	"messageHistoryBytes": {48 << 10, "bytes"}, "messageHistoryAgeSeconds": {30 * 24 * 60 * 60, "seconds"},
	"transferHistoryEntries": {32, "entries"}, "receiveWaitSeconds": {10 * 60, "seconds"}, "fileTransferSeconds": {10 * 60, "seconds"},
	"stagingSeconds": {600, "seconds"},
}
var resourceDefinitions = map[string]Definition{
	"relayPresenceConnections": {4, "connections"}, "relayCandidateAttempts": {4, "attempts"},
	"relayTLSConnections": {64, "connections"}, "relayAdmissionConnections": {16, "connections"},
	"workerFrameBytes": {1 << 20, "bytes"}, "workerRequests": {128, "requests"}, "workerHandles": {1024, "handles"},
	"stagingInventoryEntries": {100000, "entries"}, "stagingInventoryDepth": {64, "levels"},
	"messageStorageBytes": {4 << 20, "bytes"}, "messageTextBytes": {16 << 10, "bytes"},
	"profileBytes": {4 << 20, "bytes"}, "lanStateBytes": {2 << 20, "bytes"}, "discoveryBytes": {256 << 10, "bytes"},
	"materializedListeners": {64, "listeners"}, "tcpConnections": {512, "connections"}, "tcpPerPolicy": {128, "connections"}, "tcpPerPeer": {64, "connections"},
	"udpSessions": {512, "sessions"}, "udpPerPolicy": {256, "sessions"}, "udpQueuedBytes": {16 << 20, "bytes"}, "udpPolicyQueuedBytes": {1 << 20, "bytes"}, "udpQueuePackets": {64, "packets"},
	"diskReserveBytes": {512 << 20, "bytes"}, "transferSpoolBytes": {4 << 30, "bytes"}, "receiveReservedBytes": {4 << 30, "bytes"}, "transferManifestBytes": {256 << 10, "bytes"}, "transferMetadataBytes": {1 << 20, "bytes"},
	"transferPending": {32, "entries"}, "transferPendingPerPeer": {8, "entries"}, "transferConcurrentFiles": {4, "streams"}, "transferConcurrentPerPeer": {2, "streams"},
	"pageBytes": {1 << 20, "bytes"}, "pageEntries": {128, "entries"},
}

func Catalog() map[string]map[string]Definition {
	copyMap := func(in map[string]Definition) map[string]Definition {
		out := map[string]Definition{}
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	return map[string]map[string]Definition{"logical": copyMap(logicalDefinitions), "resources": copyMap(resourceDefinitions)}
}

func Defaults() Policy {
	return Policy{Version: Version, Logical: map[string]Choice{}, Resources: map[string]Choice{}}
}

func (p Policy) Validate() error {
	if p.Version != Version {
		return errors.New("unsupported capacity policy version")
	}
	for _, group := range []struct {
		values      map[string]Choice
		definitions map[string]Definition
		logical     bool
	}{{p.Logical, logicalDefinitions, true}, {p.Resources, resourceDefinitions, false}} {
		for key, choice := range group.values {
			definition, known := group.definitions[key]
			if !known {
				return fmt.Errorf("unknown capacity policy %q", key)
			}
			if err := choice.Validate(group.logical); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			if definition.Unit == "seconds" && choice.Value != nil && *choice.Value > MaxDurationSeconds {
				return fmt.Errorf("%s exceeds the time representation", key)
			}
		}
	}
	return nil
}

func (p Policy) Resolve() (Policy, error) {
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	out := Defaults()
	for _, group := range []struct {
		input, output map[string]Choice
		definitions   map[string]Definition
	}{{p.Logical, out.Logical, logicalDefinitions}, {p.Resources, out.Resources, resourceDefinitions}} {
		for key, definition := range group.definitions {
			choice, ok := group.input[key]
			if !ok || choice.Mode == "default" {
				choice = Limited(definition.Default)
			}
			if choice.Value != nil {
				n := *choice.Value
				choice.Value = &n
			}
			group.output[key] = choice
		}
	}
	return out, nil
}

func (p Policy) Number(group, key string) int64 {
	m := p.Logical
	if group == "resources" {
		m = p.Resources
	}
	definitions := logicalDefinitions
	if group == "resources" {
		definitions = resourceDefinitions
	}
	if _, known := definitions[key]; !known {
		return 0
	}
	c, ok := m[key]
	if !ok || c.Mode == "default" {
		r, err := p.Resolve()
		if err != nil {
			return 0
		}
		return r.Number(group, key)
	}
	if c.Mode == "unlimited" {
		return math.MaxInt64
	}
	if c.Value == nil {
		return 0
	}
	return *c.Value
}

func (p Policy) Clone() Policy {
	out := Policy{Version: p.Version, Logical: map[string]Choice{}, Resources: map[string]Choice{}}
	for key, value := range p.Logical {
		if value.Value != nil {
			n := *value.Value
			value.Value = &n
		}
		out.Logical[key] = value
	}
	for key, value := range p.Resources {
		if value.Value != nil {
			n := *value.Value
			value.Value = &n
		}
		out.Resources[key] = value
	}
	return out
}

func Duration(seconds int64) (time.Duration, error) {
	if seconds < 1 || seconds > MaxDurationSeconds {
		return 0, errors.New("duration must be positive whole seconds within the time representation")
	}
	return time.Duration(seconds) * time.Second, nil
}
