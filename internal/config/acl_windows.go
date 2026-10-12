//go:build windows

package config

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// localSystemSID is already granted by the fixed "SY" entry.
const localSystemSID = "S-1-5-18"

// dataDirSDDL is the protected DACL for the data directory: full control,
// inherited by files and subdirectories, for SYSTEM, Administrators and the
// owning user.
func dataDirSDDL(ownerSID string) string {
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if ownerSID != localSystemSID {
		sddl += "(A;OICI;FA;;;" + ownerSID + ")"
	}
	return sddl
}

// currentUserSID returns the SID of the user running this process.
func currentUserSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read process token user: %w", err)
	}
	return u.User.Sid.String(), nil
}

// restrictRoot is restrictDir for the data directory itself.
func restrictRoot(path string) error { return restrictDir(path) }

// restrictDir replaces the DACL of path with dataDirSDDL, blocking inheritance
// from the parent.
func restrictDir(path string) error {
	owner, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(dataDirSDDL(owner))
	if err != nil {
		return fmt.Errorf("build security descriptor: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read DACL: %w", err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("set DACL: %w", err)
	}
	return nil
}
