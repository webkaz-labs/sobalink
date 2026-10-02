package main

import "golang.org/x/sys/windows"

// The bounded reader uses UV's cancellable byte stream. ReadFile follows the
// console input code page, so temporarily choose UTF-8 and restore it afterward.
func prepareEditorEncoding() (func() error, error) {
	original, err := windows.GetConsoleCP()
	if err != nil {
		return nil, err
	}
	if err = windows.SetConsoleCP(65001); err != nil {
		return nil, err
	}
	return func() error { return windows.SetConsoleCP(original) }, nil
}
