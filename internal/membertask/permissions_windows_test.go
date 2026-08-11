//go:build windows

package membertask

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestProfilePermissionsRejectAnotherWindowsPrincipal(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "member_task_secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SecureProfilePermissions(root, secret); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if err := windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, flags, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProfilePermissions(root, secret); err == nil {
		t.Fatal("insecure Everyone DACL was accepted")
	}
}

func TestWindowsDACLProtectionFlagIsRequired(t *testing.T) {
	protected, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	unprotected, err := windows.SecurityDescriptorFromString("D:(A;;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	if !isDACLProtected(protected) {
		t.Fatal("protected DACL was not recognized")
	}
	if isDACLProtected(unprotected) {
		t.Fatal("unprotected DACL was accepted")
	}
}

func TestSecureProfilePermissionsPreservesCurrentUserAccessToRuntimeFiles(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "member_task_secret")
	runtimeFile := filepath.Join(root, "app.log")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SecureProfilePermissions(root, secret); err != nil {
		t.Fatal(err)
	}

	userSID, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(runtimeFile, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE {
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if sid.String() == userSID {
				return
			}
		}
	}
	t.Fatalf("runtime file DACL does not grant current user %s access", userSID)
}
