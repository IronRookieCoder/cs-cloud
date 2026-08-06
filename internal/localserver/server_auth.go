package localserver

import (
	"crypto/rand"
	"encoding/hex"

	"cs-cloud/internal/config"
	"cs-cloud/internal/logger"
)

// ensureLocalAPIKey generates an ephemeral random API key when cfg has none.
// authMiddleware is a no-op while the key is empty, so without this a default
// deployment exposes task-completion / approve / reject endpoints to any local
// process. The generated key is process-scoped: the daemon injects it into
// each task's env (CS_CLOUD_LOCAL_API_KEY via writeTaskEnvFile), so in-task CLI
// callbacks authenticate with no operator configuration. Restarting the daemon
// rotates the key, which is fine because tasks read the current key from env.
//
// To pin a stable key, set CS_CLOUD_API_KEY (or config.json api_key).
func ensureLocalAPIKey(cfg *config.Config) {
	if cfg == nil || cfg.APIKey != "" {
		return
	}
	b := make([]byte, 24) // 192-bit
	if _, err := rand.Read(b); err != nil {
		// crypto/rand should not fail; if it does, keep the server running
		// (unauthenticated) rather than refusing to start.
		logger.Warn("localserver: failed to generate an ephemeral api key (%v); server stays unauthenticated. Set CS_CLOUD_API_KEY to enable auth.", err)
		return
	}
	cfg.APIKey = hex.EncodeToString(b)
	logger.Info("localserver: no api key configured; generated an ephemeral key for this process. Set CS_CLOUD_API_KEY to pin it.")
}
