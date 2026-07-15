package runtime

// PersistentDriver is a long-lived driver managed by AgentManager.
// Unlike normal agent drivers that create per-conversation Agent processes,
// persistent drivers are started once and run for the lifetime of the daemon.
type PersistentDriver interface {
	Name() string
	Start() error
	Stop() error
	Health() error
}
