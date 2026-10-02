//go:build !windows

package main

func prepareEditorEncoding() (func() error, error) { return func() error { return nil }, nil }
