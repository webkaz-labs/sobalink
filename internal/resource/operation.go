package resource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

const LocalActor = "local-control"

type ApplyRequest struct {
	Target
	OperationID  string   `json:"operationId"`
	BaseRevision string   `json:"baseRevision"`
	Revision     string   `json:"revision"`
	Settings     Settings `json:"settings"`
}
type StatusRequest struct {
	Target
	OperationID string `json:"operationId"`
}

// Outcome is historical evidence, never an assertion about current settings.
// Stages contain fixed enums, never raw provider errors or private filenames.
type Outcome struct {
	Status        string `json:"status"`
	Configuration string `json:"configuration"`
	Accounting    string `json:"accounting"`
	Transfer      string `json:"transfer"`
}
type Record struct {
	Request     ApplyRequest `json:"request"`
	Actor       string       `json:"actor"`
	RequestHash string       `json:"requestHash"`
	Phase       string       `json:"phase"`
	Outcome     Outcome      `json:"outcome"`
}
type JournalUsage struct {
	Records    int  `json:"records"`
	Bytes      int  `json:"bytes"`
	MaxRecords int  `json:"maxRecords"`
	MaxBytes   int  `json:"maxBytes"`
	Writable   bool `json:"writable"`
}
type Operation struct {
	Target
	OperationID     string       `json:"operationId"`
	Outcome         Outcome      `json:"outcome"`
	EvidenceDurable bool         `json:"evidenceDurable"`
	Current         Descriptor   `json:"current"`
	Journal         JournalUsage `json:"journal"`
}

func ValidDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(b) == s
}
func OperationID(id, nonce string, sequence uint64) string {
	return id + ":" + nonce + ":" + strconv.FormatUint(sequence, 10)
}
func ParseOperationID(s string) (id, nonce string, sequence uint64, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || !ValidID(parts[0]) || !ValidID(parts[1]) {
		err = errors.New("invalid operation ID")
		return
	}
	sequence, err = strconv.ParseUint(parts[2], 10, 64)
	if err != nil || sequence == 0 || strconv.FormatUint(sequence, 10) != parts[2] {
		err = errors.New("invalid operation sequence")
		return
	}
	return parts[0], parts[1], sequence, nil
}
func (r ApplyRequest) Validate() error {
	id, _, _, err := ParseOperationID(r.OperationID)
	if err != nil || id != r.ResourceID || r.Target.Validate() != nil || !ValidDigest(r.BaseRevision) || !ValidDigest(r.Revision) || r.Settings.Validate() != nil {
		return errors.New("invalid resource operation")
	}
	return nil
}
func RequestHash(r ApplyRequest) string {
	encoded, _ := json.Marshal(struct {
		Actor   string       `json:"actor"`
		Action  string       `json:"action"`
		Request ApplyRequest `json:"request"`
	}{LocalActor, "apply", r})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
func UnknownOutcome() Outcome { return Outcome{"unknown", "unobserved", "unobserved", "unobserved"} }
func (o Outcome) Validate() error {
	valid := false
	switch o.Status {
	case "applied":
		valid = o.Configuration == "durable" && successfulStage(o.Accounting) && successfulStage(o.Transfer)
	case "failed":
		valid = (o.Configuration == "not_attempted" || o.Configuration == "not_published") && o.Accounting == "not_attempted" && o.Transfer == "not_attempted"
	case "canceled":
		valid = o.Configuration == "not_attempted" && o.Accounting == "not_attempted" && o.Transfer == "not_attempted"
	case "saved_not_applied":
		valid = o.Configuration == "durable" && ((o.Accounting == "failed" && o.Transfer == "not_attempted") || (successfulStage(o.Accounting) && o.Transfer == "failed"))
	case "unknown":
		valid = o == UnknownOutcome() || o.Configuration == "uncertain" && ((o.Accounting == "failed" && o.Transfer == "not_attempted") || successfulStage(o.Accounting) && (successfulStage(o.Transfer) || o.Transfer == "failed"))
	}
	if !valid {
		return errors.New("invalid resource outcome")
	}
	return nil
}
func successfulStage(stage string) bool { return stage == "succeeded" || stage == "not_required" }
func (r Record) Validate(id string, highWater uint64) error {
	_, _, seq, err := ParseOperationID(r.Request.OperationID)
	if err != nil || r.Request.Validate() != nil || r.Request.ResourceID != id || seq > highWater || r.Actor != LocalActor || r.RequestHash != RequestHash(r.Request) || r.Outcome.Validate() != nil {
		return errors.New("invalid operation record")
	}
	if r.Phase != "intent" && r.Phase != "result" || r.Phase == "intent" && r.Outcome != UnknownOutcome() {
		return errors.New("invalid operation phase")
	}
	return nil
}
func (r Record) Pinned() bool { return r.Phase == "intent" || r.Outcome.Status == "unknown" }
