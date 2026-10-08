//go:build web_activation_native && managed_restart_native && linux && !product_activation_native

package main

import "os/exec"

func runProductActivationDispatch(string) int                                       { return 97 }
func prepareProductSupervisor(string, *exec.Cmd) (activationSupervisorExtra, error) { return nil, nil }
func productSelfOriginAllowed(*activationChild) bool                                { return false }

func runProductBrowserScope() int { return 97 }
