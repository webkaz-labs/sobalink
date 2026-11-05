//go:build soba_e2e

package main

import (
	"context"
	"errors"
	"os"
	"syscall"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// This sidecar contains only closed-vocabulary facts. It is separate from the
// private session and from ignored child stdout/stderr; raw errors never enter it.
type startupDiagnostic struct {
	Stage    string `json:"stage"`
	Category string `json:"category"`
}

// Cleanup can join an additional error after startup failed. Do not attribute
// an arbitrary branch's classification to the startup stage.
func multipleStartupCauses(err error) bool {
	switch value := err.(type) {
	case interface{ Unwrap() []error }:
		count := 0
		for _, child := range value.Unwrap() {
			if child != nil {
				count++
				if multipleStartupCauses(child) {
					return true
				}
			}
		}
		return count > 1
	case interface{ Unwrap() error }:
		return multipleStartupCauses(value.Unwrap())
	default:
		return false
	}
}

func startupErrorCategory(err error) string {
	if multipleStartupCauses(err) {
		return "multiple-causes"
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		switch code := coded.ErrorCode(); code {
		case "lan_listener_conflict", "lan_listener_address_unavailable", "lan_listener_permission_denied", "lan_state_capacity", "lan_listener_capacity", "lan_start_failed":
			return code
		}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, os.ErrPermission):
		return "permission"
	case errors.Is(err, syscall.EADDRINUSE):
		return "address-in-use"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "address-unavailable"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection-refused"
	case errors.Is(err, os.ErrNotExist):
		return "not-exist"
	default:
		return "unclassified"
	}
}

func writeStartupDiagnostic(path, stage string, err error) error {
	switch stage {
	case "arguments", "temporary-directory", "fixture-files", "primary-lock", "primary-core", "receive-settings", "secondary-lock", "secondary-core", "network-configure", "peer-trust", "peer-reconnect", "seed-messages", "seed-transfer", "seed-share", "discovery-refresh", "assets", "service-reservation", "web-server", "access-code", "session-write":
	default:
		stage = "unknown"
	}
	return config.WriteJSON(path, startupDiagnostic{Stage: stage, Category: startupErrorCategory(err)})
}
