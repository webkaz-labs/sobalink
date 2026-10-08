// Package resource defines the narrow, local transfer-settings contract.
package resource

import (
	"encoding/hex"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/capacity"
)

const SchemaVersion = 1
const Type = "transfer-admission-settings"

type Settings struct {
	TransferConcurrentFiles   capacity.Choice `json:"transferConcurrentFiles"`
	TransferConcurrentPerPeer capacity.Choice `json:"transferConcurrentPerPeer"`
}

func (s Settings) Validate() error {
	if err := s.TransferConcurrentFiles.Validate(false); err != nil {
		return err
	}
	return s.TransferConcurrentPerPeer.Validate(false)
}

type Effective struct {
	TransferConcurrentFiles   int64 `json:"transferConcurrentFiles"`
	TransferConcurrentPerPeer int64 `json:"transferConcurrentPerPeer"`
}
type Target struct {
	SchemaVersion int    `json:"schemaVersion"`
	ResourceID    string `json:"resourceId"`
}

func (t Target) Validate() error {
	if t.SchemaVersion != SchemaVersion || !ValidID(t.ResourceID) {
		return errors.New("invalid resource target or schema")
	}
	return nil
}

type PreviewRequest struct {
	Target
	Settings Settings `json:"settings"`
}
type Descriptor struct {
	Target
	Type       string    `json:"type"`
	Authority  string    `json:"authority"`
	Provider   string    `json:"provider"`
	Operations []string  `json:"operations"`
	Revision   string    `json:"revision"`
	Requested  Settings  `json:"requested"`
	Effective  Effective `json:"effective"`
}
type Catalog struct {
	SchemaVersion int          `json:"schemaVersion"`
	Resources     []Descriptor `json:"resources"`
}
type Preview struct {
	Target
	OperationID  string    `json:"operationId"`
	BaseRevision string    `json:"baseRevision"`
	Revision     string    `json:"revision"`
	Requested    Settings  `json:"requested"`
	Effective    Effective `json:"effective"`
	Destructive  bool      `json:"destructive"`
}

func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	b, err := hex.DecodeString(id)
	return err == nil && hex.EncodeToString(b) == id
}
