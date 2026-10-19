//go:build windows

package main

import "golang.org/x/sys/windows"

// protect replaces path's DACL with one entry: full control for the account
// running this, inherited by anything inside. Mode bits mean nothing on
// Windows; without this a key under C:\work would carry that directory's
// ACL, which commonly lets every authenticated user read it.
func protect(path string) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
