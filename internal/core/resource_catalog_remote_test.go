package core

import (
	"context"
	"errors"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
)

func TestResourceCatalogOnlyExplicitRemoteUnsupported(t *testing.T) {
	s, _, _ := catalogLocalFixture(t, resourcecatalog.LocalService)
	for _, err := range []error{errors.New("unsupported"), context.DeadlineExceeded, resourceManagementUnavailable(), resourceRemoteUnavailable()} {
		if resourceCatalogRemoteOutcome(s, err).State != "unavailable" {
			t.Fatal("ambiguous transport failure called unsupported")
		}
	}
	for _, code := range []string{"resource_remote_unsupported", "resource_management_remote_unsupported"} {
		if resourceCatalogRemoteOutcome(s, &localCommandError{code, "synthetic authenticated unsupported"}).State != "unsupported" {
			t.Fatal("explicit unsupported lost")
		}
	}
}

func TestResourceCatalogRemoteBudgetPrecedesSelection(t *testing.T) {
	s, l, _ := catalogLocalFixture(t, resourcecatalog.LocalService)
	// A zero-budget call cannot touch even an uninitialized Core, capture an
	// origin, make a wire exchange or fall back to another protocol.
	c := &Core{}
	for _, kind := range []string{resourcecatalog.RemoteSettingsV1, resourcecatalog.RemoteSettingsV2} {
		s.Kind, s.SourceID = kind, kind
		got, origin := c.resourceCatalogRemoteSettings(context.Background(), s, l, &resourceCatalogBudget{rows: 1, bytes: 1})
		if got.State != "limited" || origin != nil || len(got.Rows) != 0 {
			t.Fatal("remote work started before admission")
		}
	}
}
