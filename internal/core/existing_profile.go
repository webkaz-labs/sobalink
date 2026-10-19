package core

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// CheckExistingProfile is a read-only admission check for UI recovery. It does
// not create a directory, repair permissions, initialize state or start work.
// The child must also set RequireExistingProfile: this check is not ownership.
func CheckExistingProfile(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("state path must be a real directory")
	}
	limits, err := readCapacityPolicy(dir)
	if err != nil {
		return err
	}
	var profile Profile
	if err := readBoundedPrivateJSON(filepath.Join(dir, "sobalink.json"), limits.Number("resources", "profileBytes"), &profile); err != nil {
		return err
	}
	if err := checkDurableProfileInventory(dir, profile); err != nil {
		return err
	}
	if err := validateProfile(profile); err != nil {
		return err
	}
	return validateCapacityBackend(profile.Settings.Network, limits)
}

// The actual child load is bound to its live lifecycle owner. A preflight read
// is not permission to adopt a replacement directory or initialize absent data.
func readExistingProfileOwned(dir string, owner *config.Lock, limit int64, profile *Profile) error {
	err := owner.WithOwnershipInfo(dir, func(directory, _ os.FileInfo) error {
		file, err := config.OpenPrivateChildFileBound(dir, "sobalink.json", directory)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if limit < 1 || info.Size() > limit {
			return errors.New("existing profile exceeds its configured storage budget")
		}
		return decodeBoundedPrivateJSON(file, limit, profile)
	})
	if err != nil {
		return err
	}
	return owner.WithOwnership(dir, func() error { return nil })
}
