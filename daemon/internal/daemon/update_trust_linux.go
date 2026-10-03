//go:build linux

package daemon

// This file is the Linux-only trust policy for plugin installs.

// IsTrusted returns true on Linux where install.sh downloads the tarball
// directly, so an unattended --update can proceed without a trust prompt.
func IsTrusted() bool {
	return true
}
