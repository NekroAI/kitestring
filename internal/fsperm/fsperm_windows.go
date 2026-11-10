//go:build windows

package fsperm

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func currentUser() (*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tu.User.Sid.Copy()
}

// Private 把文件的访问控制列表设成只有当前用户完全控制，不继承上级目录的权限。
func Private(path string) error {
	sid, err := currentUser()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// IsPrivate 判断文件的访问控制列表是否只允许当前用户访问（不继承、只有一条允许项且是本人）。
func IsPrivate(path string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	ctl, _, err := sd.Control()
	if err != nil {
		return false, err
	}
	if ctl&windows.SE_DACL_PROTECTED == 0 {
		return false, nil
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false, err
	}
	sid, err := currentUser()
	if err != nil {
		return false, err
	}
	if dacl.AceCount != 1 {
		return false, nil
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return false, err
	}
	aceSid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	return ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE && aceSid.Equals(sid), nil
}
