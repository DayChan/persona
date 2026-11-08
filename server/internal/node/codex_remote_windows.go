package node

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// privateSocket: a Unix control socket (Codex's app-server on Windows serves `--listen unix://` on an AF_UNIX socket
// file too) and its directory belong to this user and let no one else in. Windows has no mode bits: the owner and the
// access list say it. Codex itself leaves its control directory to this user alone (SYSTEM and Administrators may hold
// any file anyway, as root does).
func privateSocket(path string) error {
	for _, p := range []string{filepath.Dir(path), path} {
		st, err := os.Lstat(p)
		if err != nil {
			return errors.New("Unix 入口不可访问")
		}
		if st.Mode()&os.ModeSymlink != 0 || !onlyMine(p) {
			return errors.New("Unix 入口或目录不属于当前用户，或权限过宽")
		}
		if p == path && st.Mode()&os.ModeSocket == 0 {
			return errors.New("Unix 入口不是 socket")
		}
	}
	return nil
}

// onlyMine: the file is this user's and its access list grants nothing to anyone but this user, SYSTEM and the
// Administrators group (an empty or missing list grants everything).
func onlyMine(path string) bool {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	me, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(me.User.Sid) {
		return false
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, i, &ace) != nil {
			return false
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue // a deny takes rights away; an inherit-only entry is for what the directory will hold
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(me.User.Sid) && !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			return false
		}
	}
	return true
}
