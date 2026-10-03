//go:build !linux

package daemon

// This file is the non-Linux trust policy for plugin installs.

// IsTrusted returns false on non-Linux where an interactive user may want to
// review the hash and permissions before trusting.
func IsTrusted() bool {
	return false
}
