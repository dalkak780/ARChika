//go:build linux || darwin

package app

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func physicalKey(info os.FileInfo, root, relative string) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Ino == 0 {
		return ""
	}
	var fs unix.Statfs_t
	if unix.Statfs(root, &fs) != nil {
		return ""
	}
	if !trustedFS(fs) {
		return ""
	}
	return fmt.Sprintf("local:%d:%d", st.Dev, st.Ino) + physicalBirth(st, root, relative)
}
