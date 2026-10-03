package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func TestAtomicCommittedLocalizationRetainsTypedOutcome(t *testing.T) {
	for _, cause := range []error{
		config.ErrAtomicCommitted,
		fmt.Errorf("%w: injected durability failure", config.ErrAtomicCommitted),
		fmt.Errorf("planned file was replaced at %s, but durability is uncertain; registration was not run; inspect the file before retrying: %w", "/chosen/startup-file", config.ErrAtomicCommitted),
	} {
		_, _, translate, err := prepareLocale([]string{"--lang", "ja", "export"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		localized := translate(cause)
		if !errors.Is(localized, config.ErrAtomicCommitted) || !strings.Contains(localized.Error(), "再実行する前") {
			t.Fatal("localized error lost committed outcome/reconciliation", localized)
		}
		if strings.Contains(cause.Error(), "/chosen/startup-file") && !strings.Contains(localized.Error(), "/chosen/startup-file") {
			t.Fatal("localized diagnostic changed user path", localized)
		}
	}
}
