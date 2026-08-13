//go:build !linux

package playback

import "os"

func openState(path string) (*os.File, error) {
	return os.Open(path)
}
