//go:build !linux && !darwin

package host

// The existing Unix numeric-PGID and Windows taskkill tree helpers do not
// provide a stable group lifetime/completion proof. Do not call them after (or
// concurrently with) Wait, and do not publish whole-run completion from leader
// reap. Unsupported local runs fail closed; remote connections remain usable.
// A platform-specific ownership primitive is required before enabling local
// successful teardown here. This is deliberately not a kernel-group framework.
func (ch *child) ownRun(_ func()) {
	ch.ownershipError(errGroupCompletionUnsupported)
	ch.waitErr = ch.cmd.Wait() // still exactly one Wait owner
	close(ch.reaped)
	close(ch.leaderDone)
	// Disposal, Done and external admission remain held: no group proof exists.
}
