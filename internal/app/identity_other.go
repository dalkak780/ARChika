//go:build !linux && !darwin

package app

import "os"

func physicalKey(os.FileInfo, string, string) string { return "" }
