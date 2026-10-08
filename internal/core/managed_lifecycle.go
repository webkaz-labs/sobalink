package core

// ManagedLifecycleState reports process-local exclusions to the outer launcher.
// It never clears an exclusion, replaces Core, or transfers store authority.
type ManagedLifecycleState struct {
	AttemptedNetwork bool `json:"attemptedNetwork"`
	NetworkReady     bool `json:"networkReady"`
}

func (c *Core) ManagedLifecycleState() ManagedLifecycleState {
	c.op.Lock()
	defer c.op.Unlock()
	return ManagedLifecycleState{AttemptedNetwork: c.attemptedNetwork != "", NetworkReady: c.networkReady.Load()}
}
