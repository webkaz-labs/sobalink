package resourcegroup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/webkaz-labs/sobalink/internal/resource"
)

// NewTemplate freezes both exact choices. Its content revision identifies only
// these values and this schema, never a server revision or management grant.
func NewTemplate(settings resource.Settings) (Template, error) {
	if settings.Validate() != nil {
		return Template{}, ErrInvalid
	}
	t := Template{SchemaVersion: SchemaVersion, Settings: cloneSettings(settings)}
	t.Revision = templateRevision(t)
	return t, nil
}

func (t Template) Validate() error {
	if t.SchemaVersion != SchemaVersion || t.Settings.Validate() != nil || !resource.ValidDigest(t.Revision) || t.Revision != templateRevision(t) {
		return ErrInvalid
	}
	return nil
}

func templateRevision(t Template) string {
	return digest("sobalink.resourcegroup.template.v1\x00", struct {
		SchemaVersion int               `json:"schemaVersion"`
		Settings      resource.Settings `json:"settings"`
	}{t.SchemaVersion, t.Settings})
}

func digest(domain string, body any) string {
	data, _ := json.Marshal(body)
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func (o Override) Validate() error {
	if o.TransferConcurrentFiles == nil && o.TransferConcurrentPerPeer == nil {
		return ErrInvalid
	}
	if o.TransferConcurrentFiles != nil && o.TransferConcurrentFiles.Validate(false) != nil || o.TransferConcurrentPerPeer != nil && o.TransferConcurrentPerPeer.Validate(false) != nil {
		return ErrInvalid
	}
	return nil
}

// ResolveSelection returns independent copies in exact peer-key order. Labels,
// wildcards and discovery membership cannot enter this contract. Rejecting the
// local node requires the coordinator's owned identity, unavailable here.
func ResolveSelection(s Selection) (ResolvedSelection, error) {
	if s.SchemaVersion != SchemaVersion || s.Template.Validate() != nil || len(s.Members) == 0 || len(s.Members) > MaxMembers {
		return ResolvedSelection{}, ErrInvalid
	}
	r := ResolvedSelection{SchemaVersion: SchemaVersion, Template: s.Template, Members: make([]ResolvedMember, 0, len(s.Members))}
	r.Template.Settings = cloneSettings(s.Template.Settings)
	for _, member := range s.Members {
		if !resource.ValidDigest(member.PeerKey) || member.Selector.Validate() != nil || member.Override != nil && member.Override.Validate() != nil {
			return ResolvedSelection{}, ErrInvalid
		}
		settings := cloneSettings(s.Template.Settings)
		if member.Override != nil {
			if member.Override.TransferConcurrentFiles != nil {
				settings.TransferConcurrentFiles = cloneChoice(*member.Override.TransferConcurrentFiles)
			}
			if member.Override.TransferConcurrentPerPeer != nil {
				settings.TransferConcurrentPerPeer = cloneChoice(*member.Override.TransferConcurrentPerPeer)
			}
		}
		r.Members = append(r.Members, ResolvedMember{PeerKey: member.PeerKey, Selector: member.Selector, Override: cloneOverride(member.Override), Requested: settings})
	}
	sort.Slice(r.Members, func(i, j int) bool { return r.Members[i].PeerKey < r.Members[j].PeerKey })
	for i := 1; i < len(r.Members); i++ {
		if r.Members[i-1].PeerKey == r.Members[i].PeerKey {
			return ResolvedSelection{}, ErrInvalid
		}
	}
	if !fits(s, MaxSelectionBytes) {
		return ResolvedSelection{}, ErrInvalid
	}
	return r, nil
}

func (s Selection) Validate() error {
	_, err := ResolveSelection(s)
	return err
}

func selectionFromResolved(s ResolvedSelection) Selection {
	r := Selection{SchemaVersion: s.SchemaVersion, Template: s.Template, Members: make([]Member, len(s.Members))}
	r.Template.Settings = cloneSettings(s.Template.Settings)
	for i, member := range s.Members {
		r.Members[i] = Member{PeerKey: member.PeerKey, Selector: member.Selector, Override: cloneOverride(member.Override)}
	}
	return r
}

func canonicalResolved(s ResolvedSelection) (ResolvedSelection, error) {
	if len(s.Members) == 0 || len(s.Members) > MaxMembers {
		return ResolvedSelection{}, ErrInvalid
	}
	r, err := ResolveSelection(selectionFromResolved(s))
	if err != nil {
		return ResolvedSelection{}, ErrInvalid
	}
	for _, supplied := range s.Members {
		for _, expected := range r.Members {
			if supplied.PeerKey == expected.PeerKey && !equalSettings(supplied.Requested, expected.Requested) {
				return ResolvedSelection{}, ErrInvalid
			}
		}
	}
	return r, nil
}

func (s ResolvedSelection) Validate() error {
	_, err := canonicalResolved(s)
	return err
}

func fits(body any, limit int) bool {
	data, err := json.Marshal(body)
	return err == nil && len(data) <= limit
}
