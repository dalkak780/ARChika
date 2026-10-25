//go:build !linux && !darwin

package app

import "errors"

const helperMemory = 768 << 20

func descriptorPath() string      { return "" }
func limitHelper() error          { return errors.New("archive helper requires Linux or macOS") }
func helperUsage() (int64, int64) { return 0, 0 }
func limitPlugin() error          { return limitHelper() }
