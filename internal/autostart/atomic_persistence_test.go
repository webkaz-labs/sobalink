package autostart

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func TestAtomicCommittedPlanRetainedWithoutRegistrationOrRetry(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			p, err := Build(testOptions(t, goos))
			if err != nil {
				t.Fatal(err)
			}
			writes, registrations := 0, 0
			err = apply(context.Background(), p, func(context.Context, string, ...string) error { registrations++; return nil }, func(path string, data []byte) error {
				writes++
				if err := config.AtomicWritePrivate(path, data); err != nil {
					return err
				}
				return config.ErrAtomicCommitted
			})
			content, readErr := os.ReadFile(p.Path)
			if !errors.Is(err, config.ErrAtomicCommitted) || readErr != nil || string(content) != p.Content || writes != 1 || registrations != 0 {
				t.Fatal("uncertain publication retried/removed/registered", writes, registrations, readErr, err)
			}
		})
	}
}
