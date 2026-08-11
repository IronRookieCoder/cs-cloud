//go:build windows

package membertask

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const localSystemSID = "S-1-5-18"

func SecureProfilePermissions(root, secretPath string) error {
	userSID, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("resolve current user SID: %w", err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;" + userSID + ")")
	if err != nil {
		return fmt.Errorf("build private DACL: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read private DACL: %w", err)
	}
	flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	for _, path := range []string{root, secretPath} {
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags, nil, nil, dacl, nil); err != nil {
			return fmt.Errorf("secure %s: %w", path, err)
		}
	}
	return nil
}

func ValidateProfilePermissions(root, secretPath string) error {
	userSID, err := currentUserSID()
	if err != nil {
		return newTaskError("insecure_profile_permissions", "cannot resolve current user SID", err)
	}
	allowed := map[string]struct{}{userSID: {}, localSystemSID: {}}
	for _, path := range []string{root, secretPath} {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil || sd == nil {
			return newTaskError("insecure_profile_permissions", "cannot inspect profile DACL", err)
		}
		if !isDACLProtected(sd) {
			return newTaskError("insecure_profile_permissions", "profile DACL inherits permissions", nil)
		}
		dacl, _, err := sd.DACL()
		if err != nil || dacl == nil {
			return newTaskError("insecure_profile_permissions", "profile has no protected DACL", err)
		}
		for i := uint16(0); i < dacl.AceCount; i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
				return newTaskError("insecure_profile_permissions", "cannot inspect profile DACL entry", err)
			}
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
				return newTaskError("insecure_profile_permissions", "profile DACL contains an unsupported entry", nil)
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			raw := sid.String()
			if _, ok := allowed[raw]; !ok {
				return newTaskError("insecure_profile_permissions", "profile DACL grants another principal access", nil)
			}
		}
	}
	return nil
}

func isDACLProtected(sd *windows.SECURITY_DESCRIPTOR) bool {
	if sd == nil {
		return false
	}
	sddl := sd.String()
	start := strings.Index(sddl, "D:")
	if start < 0 {
		return false
	}
	flags := sddl[start+2:]
	if end := strings.IndexByte(flags, '('); end >= 0 {
		flags = flags[:end]
	}
	return strings.Contains(flags, "P")
}

func currentUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}
