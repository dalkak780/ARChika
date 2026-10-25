//go:build linux || darwin

package app

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func LockDirectory(directory string) (*os.File, error) {
	if e := os.MkdirAll(directory, 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(directory, ".archika.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("application directory is already in use by another process")
	}
	return f, nil
}
