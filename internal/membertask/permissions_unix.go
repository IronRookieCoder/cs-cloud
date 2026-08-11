//go:build !windows

package membertask

import (
	"fmt"
	"os"
)

func SecureProfilePermissions(root, secretPath string) error {
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("secure profile root: %w", err)
	}
	if err := os.Chmod(secretPath, 0o600); err != nil {
		return fmt.Errorf("secure member task secret: %w", err)
	}
	return nil
}

func ValidateProfilePermissions(root, secretPath string) error {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return newTaskError("insecure_profile_permissions", "cannot inspect profile root permissions", err)
	}
	if rootInfo.Mode().Perm() != 0o700 {
		return newTaskError("insecure_profile_permissions", "profile root permissions must be 0700", nil)
	}
	secretInfo, err := os.Stat(secretPath)
	if err != nil {
		return newTaskError("insecure_profile_permissions", "cannot inspect member task secret permissions", err)
	}
	if secretInfo.Mode().Perm() != 0o600 {
		return newTaskError("insecure_profile_permissions", "member task secret permissions must be 0600", nil)
	}
	return nil
}
