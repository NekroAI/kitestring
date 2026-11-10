//go:build !windows

package fsperm

import "os"

// Private 把文件改成只有本人能读写。
func Private(path string) error { return os.Chmod(path, 0o600) }

// IsPrivate 判断文件是否只有本人能读写。
func IsPrivate(path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return st.Mode().Perm()&0o077 == 0, nil
}
