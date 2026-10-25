//go:build linux || darwin

package app

import (
	"os"
	"os/exec"
	"syscall"
)

func isolateProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
func PluginExecMain(path string) error {
	if e := limitPlugin(); e != nil {
		return e
	}
	return syscall.Exec(path, []string{path}, os.Environ())
}
