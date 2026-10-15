package distribution

import (
	"fmt"
	"os"
	"path/filepath"
)

// copyDocumentationVisuals includes only the rendered SVGs used by the shipped
// guides. Do not recursively copy assets: generators, previews and caches are
// development inputs, not offline documentation. readSourceFile rejects links
// in every path component before reading the selected files.
func copyDocumentationVisuals(root, share string) error {
	const directory = "docs/assets/visuals"
	files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(directory), "*.svg"))
	if err != nil {
		return err
	}
	for _, file := range files {
		rel := directory + "/" + filepath.Base(file)
		data, err := readSourceFile(root, rel)
		if err != nil {
			return fmt.Errorf("documentation visual: %w", err)
		}
		destination := filepath.Join(share, filepath.FromSlash(rel))
		if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err = os.WriteFile(destination, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
