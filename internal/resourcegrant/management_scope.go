package resourcegrant

// ManagementScope is a strict data model. Validating it does not issue,
// upgrade, migrate, or authorize a grant. Existing Record validation remains
// inspect-only; management grants require separate explicit confirmation.
type ManagementScope struct {
	Actions []string `json:"actions"`
	Fields  []string `json:"fields"`
}

const (
	PreviewAction = "preview"
	ApplyAction   = "apply"
	StatusAction  = "operation.status"
)

// Validate permits exactly the complete management action set for both existing
// transfer choices, including default. There are no arbitrary property patches,
// numeric grant bounds, partial write scopes, or wildcard permissions.
func (s ManagementScope) Validate() error {
	if len(s.Actions) != 4 || s.Actions[0] != Inspect || s.Actions[1] != PreviewAction || s.Actions[2] != ApplyAction || s.Actions[3] != StatusAction ||
		len(s.Fields) != 2 || s.Fields[0] != FilesField || s.Fields[1] != PerPeerField {
		return ErrInvalid
	}
	return nil
}
