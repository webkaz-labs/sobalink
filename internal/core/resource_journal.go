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
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

const resourceStateMaxBytes = 256 << 10
const resourceStateMaxRecords = 128

// The final envelope reserves bounded operation evidence, but this read-only
// slice accepts only zero high-water and empty records. Capacity remains the
// sole settings authority. No sidecar state is written by a preview.
type resourceEnvelope struct {
	SchemaVersion int               `json:"schemaVersion"`
	ResourceID    string            `json:"resourceId"`
	HighWater     *uint64           `json:"highWater"`
	Records       []json.RawMessage `json:"records"`
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
	if err := resource.Decode(data, resourceStateMaxBytes, &state); err != nil {
		return state, err
	}
	if state.SchemaVersion != resource.SchemaVersion || !resource.ValidID(state.ResourceID) || state.HighWater == nil || *state.HighWater != 0 || state.Records == nil || len(state.Records) != 0 {
		return state, errors.New("unsupported resource state")
	}
	return state, nil
}

// Open does not acquire process.lock. Only its actual lifecycle owner may
// initialize this identity. Every owned reopen republishes the validated state
// successfully before using it; a read alone cannot certify prior durability.
func (c *Core) initializeResourceIdentity(owner *config.Lock) {
	c.resourceIdentity, c.resourceNonce, c.resourceLock = "", "", nil
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
			state = resourceEnvelope{resource.SchemaVersion, id, &zero, []json.RawMessage{}}
		} else if err != nil {
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
		c.resourceDirectoryIdentity = binding.journalInfo
		return nil
	})
	if err != nil {
		c.resourceIdentity, c.resourceNonce, c.resourceLock = "", "", nil
	}
}
