package platform

import "os"

const DefaultCloudBaseURL = "https://zgsm.sangfor.com"

const InvokerEnvKey = "CSC_CLOUD_INVOKER"

func Invoker() string {
	return os.Getenv(InvokerEnvKey)
}

func IsInvokedByCsc() bool {
	return os.Getenv(InvokerEnvKey) == "csc"
}

func Getenv(key string) string {
	return os.Getenv(key)
}

// GetenvCompat reads an environment variable by its primary name, falling
// back to a legacy name when the primary is unset or empty. Used to rename
// env vars (e.g. CS_CLOUD_* → CS_BRIDGE_*) without breaking existing
// deployments: the new name wins, but old invocations keep working.
func GetenvCompat(primary, legacy string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	return os.Getenv(legacy)
}
