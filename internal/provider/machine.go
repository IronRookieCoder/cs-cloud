package provider

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/user"
	"runtime"
	"sort"
	"strings"
)

// MachineIDParts returns machine identity components for informational display.
// It is NOT used for device_id generation (which is random + persisted).
func MachineIDParts() (platform, macAddrs, hostname, username string) {
	macAddrs = getMACAddresses()
	hostname, _ = os.Hostname()
	username = "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		username = stripDomain(u.Username)
	}
	platform = jsPlatform()
	return
}

// GenerateMachineID generates a unique device identifier.
// Determinism is not needed — the result is persisted in device.json
// and becomes the authoritative source on subsequent runs.
func GenerateMachineID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Fallback: hash machine characteristics if crypto/rand fails
		platform, macAddrs, hostname, username := MachineIDParts()
		raw := fmt.Sprintf("%s-%s-%s-%s", platform, macAddrs, hostname, username)
		h := sha256.Sum256([]byte(raw))
		return fmt.Sprintf("%x", h)
	}
	return hex.EncodeToString(b)
}

// GenerateOldMachineID replicates the original deterministic hash algorithm
// (SHA256 of platform + first MAC + username). Used exclusively as the
// legacyDeviceId in registration requests for server-side device migration.
func GenerateOldMachineID() string {
	platform := jsPlatform()
	mac := getFirstMACAddress()
	username := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		username = stripDomain(u.Username)
	}
	raw := fmt.Sprintf("%s-%s-%s", platform, mac, username)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h)
}

func GenerateLegacyMachineID() string {
	platform := jsPlatform()
	hostname, _ := os.Hostname()
	username := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		username = stripDomain(u.Username)
	}
	raw := fmt.Sprintf("%s-%s-%s", platform, hostname, username)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h)
}

func getMACAddresses() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "unknown"
	}
	var addrs []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 || len(iface.HardwareAddr) == 0 {
			continue
		}
		addrs = append(addrs, iface.HardwareAddr.String())
	}
	if len(addrs) == 0 {
		return "unknown"
	}
	sort.Strings(addrs)
	var b strings.Builder
	for _, a := range addrs {
		b.WriteString(strings.ToLower(strings.ReplaceAll(a, ":", "")))
	}
	return b.String()
}

// getFirstMACAddress replicates the ORIGINAL single-MAC selection behavior.
// Returns the first sorted non-loopback MAC address, lowercased, colons stripped.
func getFirstMACAddress() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "unknown"
	}
	var addrs []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 || len(iface.HardwareAddr) == 0 {
			continue
		}
		addrs = append(addrs, iface.HardwareAddr.String())
	}
	if len(addrs) == 0 {
		return "unknown"
	}
	sort.Strings(addrs)
	return strings.ToLower(strings.ReplaceAll(addrs[0], ":", ""))
}

func JSPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	default:
		return runtime.GOOS
	}
}

func jsPlatform() string { return JSPlatform() }

func stripDomain(s string) string {
	if idx := strings.LastIndex(s, `\`); idx >= 0 {
		return s[idx+1:]
	}
	if idx := strings.LastIndex(s, `/`); idx >= 0 {
		return s[idx+1:]
	}
	return s
}
