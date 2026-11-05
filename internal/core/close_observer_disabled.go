//go:build !product_activation_native || !web_activation_native || !managed_restart_native || !linux

package core

type productCloseObservation struct{}

func (*Core) observeProductClose(_ string, err error) error { return err }
