//go:build !linux && !darwin

package app

import (
	"errors"
	"os/exec"
)

func isolateProcess(cmd *exec.Cmd) {}
func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
func PluginExecMain(string) error { return errors.New("plugin processes require Linux or macOS") }
