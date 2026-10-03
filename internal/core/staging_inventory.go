package core

import (
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
)

// Restarted payloads are not resumed or removed. Their retained bytes still
// consume staging capacity until the user reviews and removes them separately.
func (c *Core) inventoryOutgoingSpool() {
	bytes, entries, err := inspectStaging(filepath.Join(c.dir, "outgoing"), c.limit("resources", "stagingInventoryEntries"), c.limit("resources", "stagingInventoryDepth"))
	c.orphanSpoolBytes, c.orphanSpoolEntries = bytes, entries
	if err != nil {
		c.orphanSpoolError = "retained staging needs inspection before new files can be staged; review the staging inventory budgets and restart after resolving the inspection limit"
	}
}

func inspectStaging(path string, maxEntries, maxDepth int64) (int64, int64, error) {
	if maxEntries < 1 || maxDepth < 1 {
		return 0, 0, errors.New("positive staging inventory budgets required")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return 0, 0, errors.New("staging root must be a directory, not a symlink")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return 0, 0, err
	}
	defer root.Close()
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		return 0, 0, errors.New("staging root changed during inventory")
	}
	var bytes, entries int64
	var visit func(*os.Root, int64) error
	visit = func(dir *os.Root, depth int64) error {
		f, err := dir.Open(".")
		if err != nil {
			return err
		}
		defer f.Close()
		for {
			children, readErr := f.ReadDir(128)
			for _, child := range children {
				if entries >= maxEntries {
					return errors.New("staging inventory entry budget exceeded")
				}
				info, err := dir.Lstat(child.Name())
				if err != nil {
					return err
				}
				entries++
				if info.IsDir() {
					if depth >= maxDepth {
						return errors.New("staging inventory depth budget exceeded")
					}
					nested, err := dir.OpenRoot(child.Name())
					if err != nil {
						return err
					}
					opened, statErr := nested.Stat(".")
					current, currentErr := dir.Lstat(child.Name())
					if statErr != nil || currentErr != nil || !current.IsDir() || !os.SameFile(info, opened) || !os.SameFile(opened, current) {
						_ = nested.Close()
						return errors.New("staging directory changed during inventory")
					}
					err = visit(nested, depth+1)
					_ = nested.Close()
					if err != nil {
						return err
					}
					continue
				}
				// Lstat charges link metadata without opening a link target.
				if info.Size() < 0 || info.Size() > math.MaxInt64-bytes {
					return errors.New("staging byte inventory overflow")
				}
				bytes += info.Size()
				if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
					return errors.New("special file in staging requires inspection")
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	err = visit(root, 0)
	return bytes, entries, err
}
