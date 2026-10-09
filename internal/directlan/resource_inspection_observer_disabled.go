//go:build !resource_inspection_native

package directlan

import "context"

// No observation or timing work outside the explicit native acceptance build.
type inspectionObservation struct{}

func beginInspectionObservation(context.Context) inspectionObservation {
	return inspectionObservation{}
}
func (inspectionObservation) finish(string, error, error) {}

func markInspectionDial(context.Context, string, bool, bool) {}
