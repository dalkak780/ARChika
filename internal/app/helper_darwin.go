package app

import "golang.org/x/sys/unix"

const helperMemory = 768 << 20

func descriptorPath() string { return "/dev/fd/3" }
func limitHelper() error {
	// Darwin does not enforce RLIMIT_DATA for mmap. Production memory isolation
	// is the Linux container; development still has process and wall-time isolation.
	return unix.Setrlimit(unix.RLIMIT_CPU, &unix.Rlimit{Cur: 120, Max: 120})
}
func helperUsage() (int64, int64) {
	var r unix.Rusage
	if unix.Getrusage(unix.RUSAGE_SELF, &r) != nil {
		return 0, 0
	}
	cpu := r.Utime.Sec*1000000 + int64(r.Utime.Usec) + r.Stime.Sec*1000000 + int64(r.Stime.Usec)
	return cpu, r.Maxrss
}
func limitPlugin() error { return limitHelper() }
