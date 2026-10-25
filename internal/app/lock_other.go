//go:build !linux && !darwin

package app

import (
	"errors"
	"os"
)

func LockDirectory(string) (*os.File, error) {
	return nil, errors.New("ARChika runtime requires Linux or macOS")
}
