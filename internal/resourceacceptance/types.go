// Package resourceacceptance provides passive, bounded observations for one
// separately gated synthetic native fixture. Observations grant no authority.
package resourceacceptance

type Role uint8

const (
	Controller Role = iota + 1
	TargetA
	TargetB
)

type Kind uint8

const (
	GroupAccepted Kind = iota + 1
	GroupIntent
	ManagementClientInvoked
	ManagementFrameAttempted
	ManagementFrameWritten
	ProviderAdmitted
)

type Action uint8

const (
	None Action = iota
	Inspect
	Preview
	Apply
	Status
)

const MaxEvents = 128

// Owner contains only the fixture's actual Core and Node pointer aliases.
// The recorder never reads their fields or renders their pointer addresses.
type Owner struct{ Core, Node any }

type Event struct {
	Sequence    uint16
	Role        Role
	Kind        Kind
	Action      Action
	RunID       string
	OperationID string
}

type View struct {
	Events            [MaxEvents]Event
	Count             uint16
	InvalidOrOverflow bool
}
