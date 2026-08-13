package discord

import (
	"fmt"
	"os"
	"path/filepath"
)

// SocketPaths returns Discord IPC candidates in the order used by Discord,
// including common Flatpak, Snap, and distribution-specific subdirectories.
func SocketPaths() []string {
	dirs := []string{os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("TMPDIR"), os.Getenv("TMP"), os.Getenv("TEMP"), "/tmp"}
	seen := map[string]bool{}
	var paths []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		for _, subdir := range []string{"", "app/com.discordapp.Discord", "snap.discord", "discord"} {
			base := filepath.Join(dir, subdir)
			for index := 0; index < 10; index++ {
				path := filepath.Join(base, fmt.Sprintf("discord-ipc-%d", index))
				if !seen[path] {
					seen[path] = true
					paths = append(paths, path)
				}
			}
		}
	}
	return paths
}
