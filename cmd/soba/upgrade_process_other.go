//go:build !linux && !darwin && !windows

package main

import "errors"

func observeUpgradeProcess(int) (upgradeProcessObserver, error) {
	return nil, errors.New("managed upgrade process supervision is unsupported on this platform")
}
