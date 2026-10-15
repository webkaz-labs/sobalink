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

// documentationScreenshots is the reviewed closed set used by the guides. Never
// glob PNGs: neighboring render previews and unreviewed captures must stay out.
var documentationScreenshots = []string{
	"docs/assets/screenshots/direct-review-en-focused.png",
	"docs/assets/screenshots/direct-review-ja-focused.png",
	"docs/assets/screenshots/message-context-en-narrow-input.png",
	"docs/assets/screenshots/message-context-en.png",
	"docs/assets/screenshots/message-context-ja-narrow-input.png",
	"docs/assets/screenshots/message-context-ja.png",
	"docs/assets/screenshots/navigation-detail-en-narrow.png",
	"docs/assets/screenshots/navigation-detail-ja-narrow.png",
	"docs/assets/screenshots/navigation-graph-en-narrow.png",
	"docs/assets/screenshots/navigation-graph-en-wide.png",
	"docs/assets/screenshots/navigation-graph-ja-narrow.png",
	"docs/assets/screenshots/navigation-graph-ja-wide.png",
	"docs/assets/screenshots/receive-damaged-en-wide.png",
	"docs/assets/screenshots/receive-damaged-ja-wide.png",
	"docs/assets/screenshots/receive-en.png",
	"docs/assets/screenshots/receive-ja.png",
	"docs/assets/screenshots/receive-legacy-en-wide.png",
	"docs/assets/screenshots/receive-legacy-ja-wide.png",
	"docs/assets/screenshots/service-review-en.png",
	"docs/assets/screenshots/service-review-ja.png",
	"docs/assets/screenshots/setup-fixed-en-1180.png",
	"docs/assets/screenshots/setup-fixed-en-390.png",
	"docs/assets/screenshots/setup-fixed-ja-1180.png",
	"docs/assets/screenshots/setup-fixed-ja-390.png",
	"docs/assets/screenshots/startup-guide-en-narrow-actions.png",
	"docs/assets/screenshots/startup-guide-en-wide.png",
	"docs/assets/screenshots/startup-guide-ja-narrow-actions.png",
	"docs/assets/screenshots/startup-guide-ja-wide.png",
	"docs/assets/screenshots/stopped-review-en-focused.png",
	"docs/assets/screenshots/stopped-review-ja-focused.png",
}

func copyDocumentationScreenshots(root, share string) error {
	for _, rel := range documentationScreenshots {
		data, err := readSourceFile(root, rel)
		if err != nil {
			return fmt.Errorf("documentation screenshot: %w", err)
		}
		if len(data) == 0 {
			return fmt.Errorf("empty documentation screenshot: %s", rel)
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
