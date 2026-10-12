//go:build windows

package config

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDataDirSDDL(t *testing.T) {
	const sid = "S-1-5-21-1-2-3-1001"
	want := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + sid + ")"
	if got := dataDirSDDL(sid); got != want {
		t.Errorf("dataDirSDDL = %q, want %q", got, want)
	}
	if got := dataDirSDDL(localSystemSID); got != "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)" {
		t.Errorf("SYSTEM owner must not duplicate the SY entry: %q", got)
	}
}

func TestEnsureDataDirAppliesProtectedDACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bubbles")
	if err := EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	owner, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	wantACEs := uint16(3)
	if owner == localSystemSID {
		wantACEs = 2
	}
	for _, p := range []string{dir, filepath.Join(dir, DirFiles), filepath.Join(dir, DirLogs)} {
		sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatalf("GetNamedSecurityInfo(%s): %v", p, err)
		}
		ctrl, _, err := sd.Control()
		if err != nil {
			t.Fatal(err)
		}
		if ctrl&windows.SE_DACL_PROTECTED == 0 {
			t.Errorf("%s: DACL is not protected from inheritance", p)
		}
		dacl, _, err := sd.DACL()
		if err != nil || dacl == nil {
			t.Fatalf("%s: DACL: %v", p, err)
		}
		if dacl.AceCount != wantACEs {
			t.Errorf("%s: DACL has %d entries, want %d", p, dacl.AceCount, wantACEs)
		}
	}

	// Files created inside inherit the restriction.
	l, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	sd, err := windows.GetNamedSecurityInfo(filepath.Join(dir, LockFileName), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("lock file DACL: %v", err)
	}
	if dacl.AceCount != wantACEs {
		t.Errorf("lock file inherited %d entries, want %d", dacl.AceCount, wantACEs)
	}
}
