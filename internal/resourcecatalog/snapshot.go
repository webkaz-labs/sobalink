package resourcecatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// SourceView distinguishes successful empty, incomplete and failed reads.
// State describes only this observation, never network reachability. Unsupported
// is supplied only for an explicit provider unsupported reply, never a timeout.
// Complete means all pages in this source revision were received, not current
// authority or an atomic multi-source read. Unknown totals remain absent.
type SourceView struct {
	Selection Selection `json:"selection"`
	State     string    `json:"state"`
	CheckedAt int64     `json:"checkedAt"`
	Revision  string    `json:"revision"`
	Complete  bool      `json:"complete"`
	Total     *int64    `json:"total,omitempty"`
	Rows      []Row     `json:"rows"`
}

func (v SourceView) validate(l Limits) error {
	if v.Selection.Validate(l) != nil || v.Rows == nil || len(v.Rows) > l.MaxRows || !oneOf(v.State, "unconfirmed", "current", "stale", "unavailable", "unsupported", "invalid", "limited") {
		return ErrInvalid
	}
	if v.State == "unconfirmed" {
		if v.CheckedAt != 0 || v.Revision != "" || v.Complete || v.Total != nil || len(v.Rows) != 0 {
			return ErrInvalid
		}
	} else if !timestamp(v.CheckedAt) && !(v.State == "invalid" && v.CheckedAt == 0) {
		return ErrInvalid
	}
	if oneOf(v.State, "unavailable", "unsupported", "invalid") {
		if v.Revision != "" || v.Complete || v.Total != nil || len(v.Rows) != 0 {
			return ErrInvalid
		}
	}
	if oneOf(v.State, "current", "stale") && !text(v.Revision, l) {
		return ErrInvalid
	}
	if v.State == "limited" && (v.Complete || v.Revision != "" && !text(v.Revision, l)) {
		return ErrInvalid
	}
	if len(v.Rows) > 0 && v.Revision == "" {
		return ErrInvalid
	}
	if v.Total != nil && (*v.Total < int64(len(v.Rows)) || *v.Total > 1<<53-1 || v.Complete && *v.Total != int64(len(v.Rows))) {
		return ErrInvalid
	}
	if v.Complete && oneOf(v.Selection.Kind, LocalSettings, RemoteSettingsV1, RemoteSettingsV2) && len(v.Rows) != 1 {
		return ErrInvalid
	}
	seen := map[Identity]bool{}
	for _, row := range v.Rows {
		if row.Validate(v.Selection, l) != nil || seen[row.Identity] {
			return ErrInvalid
		}
		seen[row.Identity] = true
	}
	return nil
}

// Snapshot is a full replacement candidate for its exact scope and selections.
// It is not a delta or a transaction across providers. An incomplete candidate
// must remain separate from the last complete view, never imply deletions.
type Snapshot struct {
	SchemaVersion int          `json:"schemaVersion"`
	ID            string       `json:"id"`
	ScopeID       string       `json:"scopeId"`
	Revision      string       `json:"revision"`
	Complete      bool         `json:"complete"`
	Sources       []SourceView `json:"sources"`
}

func (s Snapshot) Validate(l Limits) error {
	if l.Validate() != nil || s.SchemaVersion != SchemaVersion || !text(s.ID, l) || !text(s.ScopeID, l) || !digest(s.Revision) || s.Sources == nil || len(s.Sources) == 0 || len(s.Sources) > l.MaxSources {
		return ErrInvalid
	}
	selections := make([]Selection, 0, len(s.Sources))
	complete, rows := true, 0
	last := ""
	for _, source := range s.Sources {
		if source.validate(l) != nil || source.Selection.SourceID <= last {
			return ErrInvalid
		}
		last = source.Selection.SourceID
		complete = complete && source.Complete
		if len(source.Rows) > l.MaxRows-rows {
			return ErrLimited
		}
		rows += len(source.Rows)
		selections = append(selections, source.Selection)
		for i := 1; i < len(source.Rows); i++ {
			if rowKey(source.Rows[i-1]) >= rowKey(source.Rows[i]) {
				return ErrInvalid
			}
		}
	}
	if validateSelections(selections, l) != nil || s.Complete != complete {
		return ErrInvalid
	}
	data, err := encodeBounded(s, l.MaxBytes, l)
	if err != nil || len(data) > l.MaxBytes {
		return ErrLimited
	}
	revision, err := revisionOf(s, l)
	if err != nil || revision != s.Revision {
		return ErrInvalid
	}
	return nil
}

// Page is normalized source data after provider-specific validation. The full
// Selection must match the pinned source. Cursor is the requested cursor; empty
// starts a read. Revisions and checked time cannot change between pages.
type Page struct {
	Selection  Selection `json:"selection"`
	State      string    `json:"state"`
	CheckedAt  int64     `json:"checkedAt"`
	Revision   string    `json:"revision"`
	Cursor     string    `json:"cursor"`
	NextCursor string    `json:"nextCursor"`
	Total      *int64    `json:"total,omitempty"`
	Rows       []Row     `json:"rows"`
}

type sourceProgress struct {
	view       SourceView
	cursor     string
	cursors    map[string]bool
	identities map[Identity]bool
	pages      int
	closed     bool
}

type Builder struct {
	limits      Limits
	id, scopeID string
	sources     map[string]*sourceProgress
}

func NewBuilder(id, scopeID string, selections []Selection, l Limits) (*Builder, error) {
	if l.Validate() != nil || !text(id, l) || !text(scopeID, l) || validateSelections(selections, l) != nil {
		return nil, ErrInvalid
	}
	b := &Builder{limits: l, id: id, scopeID: scopeID, sources: map[string]*sourceProgress{}}
	for _, selection := range selections {
		b.sources[selection.SourceID] = &sourceProgress{view: SourceView{Selection: selection, State: "unconfirmed", Rows: []Row{}}, cursors: map[string]bool{}, identities: map[Identity]bool{}}
	}
	// Reserve failure-state and digest framing before accepting any rows. A
	// budget too small even to report each source is an error, not silent loss.
	probe := b.budgetSnapshot()
	if _, err := encodeBounded(probe, l.MaxBytes, l); err != nil {
		return nil, ErrLimited
	}
	return b, nil
}

func validateSelections(selections []Selection, l Limits) error {
	if selections == nil || len(selections) == 0 || len(selections) > l.MaxSources {
		return ErrInvalid
	}
	ids, keys, peers := map[string]bool{}, map[Selection]bool{}, map[string]bool{}
	for _, s := range selections {
		if s.Validate(l) != nil || ids[s.SourceID] {
			return ErrInvalid
		}
		ids[s.SourceID] = true
		key := s
		key.SourceID, key.Epoch, key.ProcessID, key.GrantID, key.GrantRevision = "", "", "", "", 0
		if keys[key] {
			return ErrInvalid
		}
		keys[key] = true
		if oneOf(s.Kind, RemoteSettingsV1, RemoteSettingsV2, RemoteService) {
			peers[s.PeerKey] = true
		}
	}
	if len(peers) > l.MaxRemoteTargets {
		return ErrLimited
	}
	return nil
}

// AddPage is transactional per page. Invalid/mixed/duplicate/cursor-loop data
// invalidates only that source and discards its candidate rows. A budget stop
// retains its bounded prefix, marks it limited and disables advisory workflows.
// Neither outcome changes independent completed sources. No more pages may be
// stitched onto a completed, failed or limited source; start a new builder.
func (b *Builder) AddPage(p Page) error {
	if b == nil {
		return ErrInvalid
	}
	state := b.sources[p.Selection.SourceID]
	if state == nil {
		return ErrInvalid
	}
	if state.closed {
		return ErrChanged
	}
	fail := func(err error) error { b.invalidate(state, p.CheckedAt); return err }
	l := b.limits
	if p.Selection != state.view.Selection || !timestamp(p.CheckedAt) || !oneOf(p.State, "current", "stale") || !text(p.Revision, l) || p.Rows == nil || p.Cursor != state.cursor || p.NextCursor != "" && (!text(p.NextCursor, l) || p.NextCursor == p.Cursor || state.cursors[p.NextCursor]) {
		return fail(ErrChanged)
	}
	if state.pages > 0 && (p.Revision != state.view.Revision || p.CheckedAt != state.view.CheckedAt || p.State != state.view.State || !sameTotal(p.Total, state.view.Total)) {
		return fail(ErrChanged)
	}
	if p.Total != nil && (*p.Total < 0 || *p.Total > 1<<53-1) {
		return fail(ErrInvalid)
	}
	if len(p.Rows) > l.MaxPageRows || state.pages >= l.MaxPages {
		return b.limit(state, p.CheckedAt)
	}
	if p.NextCursor != "" && len(p.Rows) == 0 {
		return fail(ErrInvalid)
	}
	seen := map[Identity]bool{}
	for _, row := range p.Rows {
		if row.Validate(p.Selection, l) != nil || state.identities[row.Identity] || seen[row.Identity] {
			return fail(ErrInvalid)
		}
		seen[row.Identity] = true
	}
	data, err := encodeBounded(p, l.MaxPageBytes, l)
	if err != nil {
		return b.limit(state, p.CheckedAt)
	}
	var copy Page
	if decodeClosed(data, l.MaxPageBytes, l, &copy) != nil {
		return fail(ErrInvalid)
	}
	count := len(copy.Rows) + len(state.view.Rows)
	if copy.Total != nil && (*copy.Total < int64(count) || copy.NextCursor == "" && *copy.Total != int64(count) || copy.NextCursor != "" && *copy.Total == int64(count)) {
		return fail(ErrInvalid)
	}
	if oneOf(p.Selection.Kind, LocalSettings, RemoteSettingsV1, RemoteSettingsV2) && (count > 1 || copy.NextCursor == "" && count != 1) {
		return fail(ErrInvalid)
	}
	rows := len(copy.Rows)
	for _, source := range b.sources {
		if len(source.view.Rows) > l.MaxRows-rows {
			return b.limit(state, p.CheckedAt)
		}
		rows += len(source.view.Rows)
	}
	previous := state.view
	nextRows := make([]Row, 0, count)
	nextRows = append(nextRows, state.view.Rows...)
	nextRows = append(nextRows, copy.Rows...)
	state.view = SourceView{Selection: copy.Selection, State: copy.State, CheckedAt: copy.CheckedAt, Revision: copy.Revision, Complete: copy.NextCursor == "", Total: copy.Total, Rows: nextRows}
	if _, err := encodeBounded(b.budgetSnapshot(), l.MaxBytes, l); err != nil {
		state.view = previous
		return b.limit(state, p.CheckedAt)
	}
	state.pages++
	state.cursors[p.Cursor] = true
	state.cursor = p.NextCursor
	for identity := range seen {
		state.identities[identity] = true
	}
	state.closed = state.view.Complete
	if !state.closed && state.pages == l.MaxPages {
		return b.limit(state, p.CheckedAt)
	}
	return nil
}

// Fail records a provider outcome without guessing absence/offline/revocation.
// The caller supplies unsupported only from an authoritative explicit response.
func (b *Builder) Fail(selection Selection, outcome string, checkedAt int64) error {
	if b == nil || !timestamp(checkedAt) || !oneOf(outcome, "unavailable", "unsupported", "invalid", "limited") {
		return ErrInvalid
	}
	state := b.sources[selection.SourceID]
	if state == nil || state.closed || state.view.Selection != selection {
		return ErrChanged
	}
	if outcome == "limited" {
		return b.limit(state, checkedAt)
	}
	state.view = SourceView{Selection: selection, State: outcome, CheckedAt: checkedAt, Rows: []Row{}}
	state.closed = true
	return nil
}
func (b *Builder) invalidate(state *sourceProgress, checkedAt int64) {
	if !timestamp(checkedAt) {
		checkedAt = state.view.CheckedAt
	}
	state.view = SourceView{Selection: state.view.Selection, State: "invalid", CheckedAt: checkedAt, Rows: []Row{}}
	state.closed = true
}
func (b *Builder) limit(state *sourceProgress, checkedAt int64) error {
	state.view.State, state.view.Complete, state.closed = "limited", false, true
	if state.view.CheckedAt == 0 {
		state.view.CheckedAt = checkedAt
	}
	return ErrLimited
}

// Reserve enough framing for every pending source to become an honest failure
// without evicting rows already completed by another source.
func (b *Builder) budgetSnapshot() Snapshot {
	s := b.snapshot()
	s.Complete = false
	for i := range s.Sources {
		s.Sources[i].State = "unavailable"
		s.Sources[i].CheckedAt = 253402300799000
		s.Sources[i].Complete = false
	}
	return s
}

func (b *Builder) snapshot() Snapshot {
	s := Snapshot{SchemaVersion: SchemaVersion, ID: b.id, ScopeID: b.scopeID, Revision: strings64Zero, Complete: true, Sources: []SourceView{}}
	for _, state := range b.sources {
		v := state.view
		v.Rows = append([]Row{}, v.Rows...)
		sort.Slice(v.Rows, func(i, j int) bool { return rowKey(v.Rows[i]) < rowKey(v.Rows[j]) })
		s.Complete = s.Complete && v.Complete
		s.Sources = append(s.Sources, v)
	}
	sort.Slice(s.Sources, func(i, j int) bool { return s.Sources[i].Selection.SourceID < s.Sources[j].Selection.SourceID })
	return s
}

// Finish returns an immutable copy. Pending sources remain unconfirmed/partial.
// A subsequent page cannot mutate a previously returned snapshot.
func (b *Builder) Finish() (Snapshot, error) {
	if b == nil {
		return Snapshot{}, ErrInvalid
	}
	s := b.snapshot()
	revision, err := revisionOf(s, b.limits)
	if err != nil {
		return Snapshot{}, err
	}
	s.Revision = revision
	data, err := EncodeSnapshot(s, b.limits)
	if err != nil {
		return Snapshot{}, err
	}
	return DecodeSnapshot(data, b.limits)
}

const strings64Zero = "0000000000000000000000000000000000000000000000000000000000000000"

func revisionOf(s Snapshot, l Limits) (string, error) {
	s.Revision = strings64Zero
	data, err := encodeBounded(s, l.MaxBytes, l)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
func digest(s string) bool {
	decoded, err := hex.DecodeString(s)
	return len(s) == 64 && err == nil && hex.EncodeToString(decoded) == s
}
func rowKey(row Row) string {
	// JSON tuple encoding avoids delimiter collisions in opaque provider IDs.
	data, _ := json.Marshal(row.Identity)
	return string(data)
}
func sameTotal(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// Cache keeps an incomplete candidate visibly separate from historical complete
// data. LastComplete is not current authority. Retention requires identical
// scope, source selection and epoch; changed authentication/re-pair/selection
// clears it. The owner must invalidate the epoch when its real checks require.
type Cache struct {
	LastComplete *Snapshot
	Candidate    Snapshot
}

func UpdateCache(previous *Snapshot, candidate Snapshot, l Limits) (Cache, error) {
	data, err := EncodeSnapshot(candidate, l)
	if err != nil {
		return Cache{}, err
	}
	copy, err := DecodeSnapshot(data, l)
	if err != nil {
		return Cache{}, err
	}
	result := Cache{Candidate: copy}
	if candidate.Complete {
		complete, err := DecodeSnapshot(data, l)
		if err != nil {
			return Cache{}, err
		}
		result.LastComplete = &complete
	} else if previous != nil && previous.Complete && previous.Validate(l) == nil && sameScope(*previous, candidate) {
		old, err := EncodeSnapshot(*previous, l)
		if err != nil {
			return Cache{}, err
		}
		complete, err := DecodeSnapshot(old, l)
		if err != nil {
			return Cache{}, err
		}
		for i := range complete.Sources {
			if complete.Sources[i].State == "current" {
				complete.Sources[i].State = "stale"
			}
		}
		// Only this derived cache-view identity changes. Original source
		// revisions, checked times, rows and completeness remain historical;
		// this digest is not a fresh provider observation or authority.
		complete.Revision, err = revisionOf(complete, l)
		if err != nil {
			return Cache{}, err
		}
		result.LastComplete = &complete
	}
	return result, nil
}
func sameScope(a, b Snapshot) bool {
	if a.ScopeID != b.ScopeID || len(a.Sources) != len(b.Sources) {
		return false
	}
	for i := range a.Sources {
		if a.Sources[i].Selection != b.Sources[i].Selection {
			return false
		}
	}
	return true
}
