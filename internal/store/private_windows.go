//go:build windows

package store

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// restrictToOwner gives path a protected owner-only DACL.
//
// Windows ignores the POSIX mode bits os.Chmod manipulates, so chmod-style
// tightening is a no-op there: the file keeps whatever its parent directory
// grants, which commonly includes BUILTIN\Users read access. The ACL is
// therefore set explicitly, mirroring how internal/apiauth protects the API
// token file on this platform.
//
// Unlike the api-token path, the database cannot be created exclusively here
// (SQLite opens it itself, and an existing database must keep its contents),
// so this creates the file if absent and then replaces its DACL.
func restrictToOwner(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create database file %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("create database file %q: %w", path, err)
	}
	if err := applyOwnerOnlyACL(path); err != nil {
		return fmt.Errorf("restrict database file %q: %w", path, err)
	}
	return nil
}

func applyOwnerOnlyACL(path string) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return fmt.Errorf("open process token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("resolve SYSTEM sid: %w", err)
	}
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(system),
			},
		},
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build owner-only ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil,
	); err != nil && !errors.Is(err, windows.ERROR_SUCCESS) {
		return fmt.Errorf("apply owner-only ACL: %w", err)
	}
	return nil
}
