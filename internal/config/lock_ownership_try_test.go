package config

import (
	"errors"
	"os"
	"testing"
)

func TestLifecycleTryOwnershipRefusesWithoutCallbackAndRetries(t *testing.T) {
	dir := t.TempDir()
	owner, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- owner.WithOwnershipInfo(dir, func(os.FileInfo, os.FileInfo) error { close(entered); <-release; return nil })
	}()
	<-entered
	called := false
	err = owner.TryWithOwnershipInfo(dir, func(os.FileInfo, os.FileInfo) error { called = true; return nil })
	if !errors.Is(err, ErrLifecycleBusy) || called {
		close(release)
		<-done
		t.Fatal("busy lifecycle operation ran callback", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := owner.TryWithOwnershipInfo(dir, func(directory, lock os.FileInfo) error {
		called = true
		actual, err := os.Stat(dir)
		if err != nil || !os.SameFile(directory, actual) || !lock.Mode().IsRegular() {
			return errors.New("wrong retained identity")
		}
		return nil
	}); err != nil || !called {
		t.Fatal("released lifecycle admission failed", err)
	}
}

func TestLifecycleTryOwnershipRetainsCloseExclusion(t *testing.T) {
	dir := t.TempDir()
	owner, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- owner.TryWithOwnershipInfo(dir, func(os.FileInfo, os.FileInfo) error { close(entered); <-release; return nil })
	}()
	<-entered
	closeStarted, closeDone := make(chan struct{}), make(chan error, 1)
	go func() { close(closeStarted); closeDone <- owner.Close() }()
	<-closeStarted
	select {
	case err := <-closeDone:
		close(release)
		<-operationDone
		t.Fatal("close escaped live owned operation", err)
	default:
	}
	close(release)
	if err := <-operationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	called := false
	err = owner.TryWithOwnershipInfo(dir, func(os.FileInfo, os.FileInfo) error { called = true; return nil })
	if err == nil || errors.Is(err, ErrLifecycleBusy) || called {
		t.Fatal("closed owner accepted operation", err)
	}
}
