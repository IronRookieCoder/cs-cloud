package workflowrunner

// isActiveEnvRoot reports whether any running task currently owns taskDir as
// its task root. The GC loop short-circuits on this so an in-flight task's
// workdir is never reclaimed — not even on the done/cancelled or 404 paths. A
// follow-up comment on an already-done issue can dispatch a task that reuses
// the prior workdir without bumping updated_at, so the TTL check alone wouldn't
// notice the resumed activity.
//
// cs-cloud does not pre-claim predicted roots (unlike the server's refcount map),
// so d.running is the exact active set. The iteration holds d.mu, which is the
// same lock reserve/release/CheckoutRepo use.
func (d *Driver) isActiveEnvRoot(taskDir string) bool {
	if taskDir == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, rec := range d.running {
		if rec != nil && rec.taskRoot == taskDir {
			return true
		}
	}
	return false
}
