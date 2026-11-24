//go:build windows

package private

import (
	"errors"
	"fmt"
	"io/fs"
	"unsafe"

	"golang.org/x/sys/windows"
)

// mkdir creates the directory with its access list already in place, so
// there is no moment in which it carries the inherited one. The list is
// protected, so nothing above it can widen it, and its one entry is inherited
// by every file and directory made inside.
func mkdir(path string) error {
	user, err := currentUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.String() + ")")
	if err != nil {
		return fmt.Errorf("describe owner-only access for %s: %w", path, err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	if err := windows.CreateDirectory(name, sa); err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS) {
			return &fs.PathError{Op: "mkdir", Path: path, Err: fs.ErrExist}
		}
		return &fs.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

// verify ignores perm: Windows does not keep modes. What it checks is the
// access list, which must exist - a null one grants everyone everything - and
// grant nothing to anyone but the account running this.
func verify(path string, _ fs.FileMode) error {
	user, err := currentUser()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read the access list of %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read the access list of %s: %w", path, err)
	}
	if dacl == nil {
		return fmt.Errorf("%s has no access list, which grants everyone full access", path)
	}
	granted := 0
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("read entry %d of the access list of %s: %w", i, path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !windows.EqualSid(sid, user) {
			return fmt.Errorf("%s grants access to %s, not only to its owner %s", path, sid, user)
		}
		granted++
	}
	if granted == 0 {
		return fmt.Errorf("%s grants its owner no access", path)
	}
	return nil
}

func currentUser() (*windows.SID, error) {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("find the account running testease: %w", err)
	}
	return tokenUser.User.Sid, nil
}
