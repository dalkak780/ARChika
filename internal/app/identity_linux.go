package app

import (
	"fmt"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func trustedFS(s unix.Statfs_t) bool {
	switch uint64(s.Type) {
	case 0xef53, 0x58465342, 0x9123683e, 0x2fc12fc1, 0x01021994:
		return true
	}
	return false
}
func physicalBirth(st *syscall.Stat_t, base, relative string) string {
	var s unix.Statx_t
	if unix.Statx(unix.AT_FDCWD, filepath.Join(base, filepath.FromSlash(relative)), unix.AT_NO_AUTOMOUNT, unix.STATX_BTIME|unix.STATX_INO, &s) != nil || s.Mask&unix.STATX_BTIME == 0 || s.Btime.Sec <= 0 || s.Ino != st.Ino || unix.Mkdev(s.Dev_major, s.Dev_minor) != uint64(st.Dev) {
		return ""
	}
	return fmt.Sprintf(":born=%d.%d", s.Btime.Sec, s.Btime.Nsec)
}
