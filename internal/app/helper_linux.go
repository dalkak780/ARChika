package app

import "golang.org/x/sys/unix"

const helperMemory = 768 << 20

func descriptorPath() string { return "/proc/self/fd/3" }
func limitHelper() error {
	if e := unix.Setrlimit(unix.RLIMIT_DATA, &unix.Rlimit{Cur: helperMemory, Max: helperMemory}); e != nil {
		return e
	}
	if e := unix.Setrlimit(unix.RLIMIT_AS, &unix.Rlimit{Cur: 2 << 30, Max: 2 << 30}); e != nil {
		return e
	}
	return unix.Setrlimit(unix.RLIMIT_CPU, &unix.Rlimit{Cur: 120, Max: 120})
}
func helperUsage() (int64, int64) {
	var r unix.Rusage
	if unix.Getrusage(unix.RUSAGE_SELF, &r) != nil {
		return 0, 0
	}
	cpu := r.Utime.Sec*1000000 + r.Utime.Usec + r.Stime.Sec*1000000 + r.Stime.Usec
	return cpu, r.Maxrss * 1024
}
func limitPlugin() error {
	if e := unix.Setrlimit(unix.RLIMIT_DATA, &unix.Rlimit{Cur: helperMemory, Max: helperMemory}); e != nil {
		return e
	}
	return unix.Setrlimit(unix.RLIMIT_CPU, &unix.Rlimit{Cur: 120, Max: 120})
}
