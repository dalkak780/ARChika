package app

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func trustedFS(s unix.Statfs_t) bool {
	b := []byte{}
	for _, c := range s.Fstypename {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b) == "apfs" || string(b) == "hfs"
}
func physicalBirth(st *syscall.Stat_t, _, _ string) string {
	if st.Birthtimespec.Sec <= 0 {
		return ""
	}
	return fmt.Sprintf(":born=%d.%d", st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
}
