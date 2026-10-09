package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/operationjournal"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

const resourceStateMaxBytes = 256 << 10
const resourceStateMaxRecords = 128

// The bounded envelope stores operation evidence, not current settings.
// Capacity remains the sole authority; preview never writes this file.
type resourceEnvelope struct {
	SchemaVersion int               `json:"schemaVersion"`
	ResourceID    string            `json:"resourceId"`
	HighWater     *uint64           `json:"highWater"`
	Records       []resource.Record `json:"records"`
	scoped        *operationjournal.EnvelopeV2
}

func resourceStatePath(dir string) string { return filepath.Join(dir, "resource-state", "state.json") }
func newResourceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func readResourceEnvelope(path string) (resourceEnvelope, error) {
	var state resourceEnvelope
	info, err := os.Lstat(path)
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Size() > resourceStateMaxBytes {
		return state, errors.New("unsafe resource state")
	}
	f, err := (transfer.Source{LocalPath: path}).Open()
	if err != nil {
		return state, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, resourceStateMaxBytes+1))
	if err != nil {
		return state, err
	}
	decoded, err := operationjournal.Decode(data)
	if err != nil {
		return state, err
	}
	if decoded.Legacy != nil {
		old := decoded.Legacy
		state = resourceEnvelope{SchemaVersion: old.SchemaVersion, ResourceID: old.ResourceID, HighWater: old.HighWater, Records: old.Records}
	} else {
		state = scopedResourceEnvelope(*decoded.Journal)
	}
	if err := state.validate(); err != nil {
		return state, err
	}
	return state, nil
}

// Open does not acquire process.lock. Only its actual lifecycle owner may
// initialize this identity. Every owned reopen republishes the validated state
// successfully before using it; a read alone cannot certify prior durability.
func (c *Core) initializeResourceIdentity(owner *config.Lock) {
	c.resourceIdentity, c.resourceNonce, c.resourceLock = "", "", nil
	c.resourceState, c.resourceFrozen = resourceEnvelope{}, false
	c.resourceRemoteEvidenceDenied = false
	c.resourceUncertainWrite = nil
	c.resourceDirectoryIdentity = nil
	err := owner.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		path := resourceStatePath(c.dir)
		if err := config.SecureChildDirectoryBound(c.dir, "resource-state", directory); err != nil {
			return err
		}
		binding, err := openResourcePathBinding(c.dir, directory, lock)
		if err != nil {
			return err
		}
		defer binding.close()
		state, err := readResourceEnvelope(path)
		if errors.Is(err, os.ErrNotExist) {
			id, idErr := newResourceID()
			if idErr != nil {
				return idErr
			}
			zero := uint64(0)
			state = resourceEnvelope{SchemaVersion: resource.SchemaVersion, ResourceID: id, HighWater: &zero, Records: []resource.Record{}}
		} else if err != nil {
			return err
		}
		state, err = state.normalizeIntents()
		if err != nil {
			return err
		}
		nonce, err := newResourceID()
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if len(encoded) > resourceStateMaxBytes {
			return errors.New("resource state is too large")
		}
		// Separate parent means resource evidence never holds the canonical settings
		// writer lease. This write deliberately does not advance authority revision.
		if err := binding.write(path, encoded, c.atomicWrite); err != nil {
			return err
		}
		c.resourceIdentity, c.resourceNonce, c.resourceLock = state.ResourceID, nonce, owner
		c.resourceState = state
		c.resourceDirectoryIdentity = binding.journalInfo
		return nil
	})
	if err != nil {
		c.resourceIdentity, c.resourceNonce, c.resourceLock = "", "", nil
	}
}

// A validated record is below this bound even with maximum finite choices,
// uint64 sequence and every longest stage. Admission reserves a whole record
// before invoking the provider, so terminal growth cannot exceed our budget.
const resourceRecordMaxBytes = 2048

func (s resourceEnvelope) validate() error {
	if s.scoped != nil {
		if s.SchemaVersion != operationjournal.FormatVersion || s.Records != nil || s.HighWater == nil || s.ResourceID != s.scoped.ResourceID || s.scoped.HighWater == nil || *s.HighWater != *s.scoped.HighWater {
			return operationjournal.ErrInvalid
		}
		return s.scoped.Validate()
	}
	if s.SchemaVersion != resource.SchemaVersion || !resource.ValidID(s.ResourceID) || s.HighWater == nil || s.Records == nil || len(s.Records) > resourceStateMaxRecords {
		return errors.New("unsupported resource state")
	}
	var previous uint64
	for _, record := range s.Records {
		if err := record.Validate(s.ResourceID, *s.HighWater); err != nil {
			return err
		}
		_, _, sequence, _ := resource.ParseOperationID(record.Request.OperationID)
		if sequence <= previous {
			return errors.New("duplicate or unordered resource sequence")
		}
		previous = sequence
		encoded, err := json.Marshal(record)
		if err != nil || len(encoded) > resourceRecordMaxBytes {
			return errors.New("oversized operation record")
		}
	}
	// Every consumed slot retains its latest evidence until a later slot is
	// admitted. Empty nonzero/high-water holes cannot be produced by this writer.
	if (*s.HighWater == 0) != (len(s.Records) == 0) || len(s.Records) != 0 && previous != *s.HighWater {
		return errors.New("invalid resource high-water")
	}
	return nil
}
func (s resourceEnvelope) clone() resourceEnvelope {
	if s.scoped != nil {
		return scopedResourceEnvelope(*s.scoped)
	}
	next := s
	high := *s.HighWater
	next.HighWater = &high
	next.Records = append([]resource.Record{}, s.Records...)
	return next
}
func (s resourceEnvelope) find(id string) (resource.Record, bool) {
	if s.scoped != nil {
		for _, record := range s.scoped.Records {
			if record.Kind == operationjournal.LocalKind && record.Local != nil && record.Local.Request.OperationID == id {
				return cloneResourceRecord(*record.Local), true
			}
		}
		return resource.Record{}, false
	}
	for _, record := range s.Records {
		if record.Request.OperationID == id {
			return record, true
		}
	}
	return resource.Record{}, false
}

// An uncertain publication has at most two exact possible journal states. These
// process-local digests never confer durability and are discarded on owned
// reopen. In particular a decoded disk result cannot replace a pinned UNKNOWN.
type resourceJournalWriteCandidates struct {
	before, attempted string
}

func (c *Core) writeResourceEnvelopeBound(state resourceEnvelope, binding *resourcePathBinding) error {
	if err := state.validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(encoded) > resourceStateMaxBytes {
		return errors.New("resource state is too large")
	}
	// No journal lease crosses the provider call. The ordinary atomic write
	// releases its private-parent admission before the canonical settings write.
	if binding == nil {
		return errResourceBinding
	}
	before := ""
	if c.resourceState.validate() == nil {
		before = resourceDigest(c.resourceState)
	}
	attempted := resourceDigest(state)
	err = binding.write(resourceStatePath(c.dir), encoded, c.atomicWrite)
	if err != nil && atomicPublished(err) {
		// Capture centrally for both local and remote journal writers only when
		// replacement occurred but durability is uncertain. Known nonpublication
		// never makes an attempted terminal result an allowed disk candidate.
		c.resourceUncertainWrite = &resourceJournalWriteCandidates{before: before, attempted: attempted}
	} else {
		c.resourceUncertainWrite = nil
	}
	return err
}
func (s resourceEnvelope) withIntent(record resource.Record) (resourceEnvelope, error) {
	if s.scoped != nil {
		next, err := operationjournal.WithIntent(*s.scoped, operationjournal.TaggedRecord{Kind: operationjournal.LocalKind, Local: &record})
		if err != nil {
			return resourceEnvelope{}, resourceJournalError(err)
		}
		return scopedResourceEnvelope(next), nil
	}
	next := s.clone()
	_, _, sequence, _ := resource.ParseOperationID(record.Request.OperationID)
	*next.HighWater = sequence
	for {
		trial := next.clone()
		trial.Records = append(trial.Records, record)
		encoded, err := json.Marshal(trial)
		recordBytes, _ := json.Marshal(record)
		if err == nil && len(trial.Records) <= resourceStateMaxRecords && len(encoded)+resourceRecordMaxBytes-len(recordBytes) <= resourceStateMaxBytes {
			return trial, trial.validate()
		}
		evict := -1
		for i, previous := range next.Records {
			if !previous.Pinned() {
				evict = i
				break
			}
		}
		if evict < 0 {
			return resourceEnvelope{}, &localCommandError{"resource_journal_full", "local resource evidence is full; unresolved operations cannot be evicted"}
		}
		next.Records = append(next.Records[:evict], next.Records[evict+1:]...)
	}
}
func (c *Core) resourceJournalUsage() resource.JournalUsage {
	encoded, _ := json.Marshal(c.resourceState)
	count := len(c.resourceState.Records)
	if c.resourceState.scoped != nil {
		count = len(c.resourceState.scoped.Records)
	}
	return resource.JournalUsage{Records: count, Bytes: len(encoded), MaxRecords: resourceStateMaxRecords, MaxBytes: resourceStateMaxBytes, Writable: !c.resourceFrozen}
}
