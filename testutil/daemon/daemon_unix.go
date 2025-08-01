//go:build !windows

package daemon

import (
	"os/exec"
	"syscall"
	"testing"

	"github.com/moby/sys/mount"
	"golang.org/x/sys/unix"
)

// cleanupMount unmounts the daemon root directory, or logs a message if
// unmounting failed.
func cleanupMount(t testing.TB, d *Daemon) {
	t.Helper()
	if err := mount.Unmount(d.Root); err != nil {
		d.log.Logf("[%s] unable to unmount daemon root (%s): %v", d.id, d.Root, err)
	}
}

// SignalDaemonDump sends a signal to the daemon to write a dump file
func SignalDaemonDump(pid int) {
	_ = unix.Kill(pid, unix.SIGQUIT)
}

func signalDaemonReload(pid int) error {
	return unix.Kill(pid, unix.SIGHUP)
}

func setsid(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
