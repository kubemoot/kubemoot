// Package confine hides the workspace volume from a program the sandbox runs.
//
// The workspace-access sidecar stages every discussion's stored results under one
// shared directory, and a program runs as the same user as the server that reads it, so
// file permissions cannot keep a program out of other discussions' files. Instead the
// program is started in its own user and mount namespace, through a re-executed copy of
// the sandbox binary (Main). There the workspace directory is replaced by the calling
// discussion's own folder, or by an empty one when the call has no discussion. The
// namespace holds no capability once the program starts, so the program cannot undo the
// mount, and from inside it the server's process is out of reach.
package confine

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// Arg is the first argument of a re-executed sandbox binary that should confine and then run a program.
const Arg = "--code-sandbox-confine"

const (
	flagMount = "--mount"
	flagEnd   = "--"
	// exitConfine is the exit code of a helper that could not set the view up.
	exitConfine = 125
	// capLimit bounds the capability numbers tried when the bounding set is emptied.
	capLimit = 64
)

// Mount replaces the directory Path with View for the program; an empty View shows an empty directory.
type Mount struct {
	Path string
	View string
}

// Command is the argument list that starts argv confined by mounts, run through the binary self.
func Command(self string, mounts []Mount, argv []string) []string {
	out := []string{self, Arg}
	for _, m := range mounts {
		out = append(out, flagMount, m.Path, m.View)
	}
	out = append(out, flagEnd)
	return append(out, argv...)
}

// Attr gives the helper process its own user and mount namespace. The current user and group map to
// themselves, so files the program works on keep their owner. Two capabilities stay ambient until the
// helper has set the view up and dropped them.
func Attr() *syscall.SysProcAttr {
	uid, gid := os.Getuid(), os.Getgid()
	return &syscall.SysProcAttr{
		Setpgid:                    true,
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		GidMappingsEnableSetgroups: false,
		AmbientCaps:                []uintptr{unix.CAP_SYS_ADMIN, unix.CAP_SETPCAP},
	}
}

// Main runs inside the helper process: it sets the view up, drops every capability and replaces itself
// with the program. It returns only by exiting, with exitConfine and a message when it could not.
func Main(args []string) {
	if err := run(args); err != nil {
		fmt.Fprintf(os.Stderr, "code-sandbox: the workspace could not be hidden from the program: %v\n", err)
		os.Exit(exitConfine)
	}
}

func run(args []string) error {
	mounts, argv, err := parse(args)
	if err != nil {
		return err
	}
	if err := apply(mounts); err != nil {
		return err
	}
	if err := dropCapabilities(); err != nil {
		return err
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}

// parse reads what Command wrote, after the Arg itself.
func parse(args []string) ([]Mount, []string, error) {
	var mounts []Mount
	for len(args) > 0 && args[0] == flagMount {
		if len(args) < 3 {
			return nil, nil, errors.New("incomplete mount")
		}
		mounts = append(mounts, Mount{Path: args[1], View: args[2]})
		args = args[3:]
	}
	if len(args) < 2 || args[0] != flagEnd {
		return nil, nil, errors.New("no program to run")
	}
	return mounts, args[1:], nil
}

func apply(mounts []Mount) error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make the mounts private: %w", err)
	}
	for _, m := range mounts {
		if err := show(m); err != nil {
			return err
		}
	}
	return nil
}

func show(m Mount) error {
	if m.View == "" {
		if err := unix.Mount("tmpfs", m.Path, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=4k,mode=0555"); err != nil {
			return fmt.Errorf("hide %s: %w", m.Path, err)
		}
		return nil
	}
	if err := unix.Mount(m.View, m.Path, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("show %s at %s: %w", m.View, m.Path, err)
	}
	return nil
}

// dropCapabilities leaves the process with none, and unable to gain any on exec.
func dropCapabilities() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	for c := 0; c < capLimit; c++ {
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(c), 0, 0, 0); err != nil {
			if errors.Is(err, unix.EINVAL) {
				break // past the last capability the kernel knows
			}
			return fmt.Errorf("drop capability %d: %w", c, err)
		}
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return fmt.Errorf("clear ambient capabilities: %w", err)
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		return fmt.Errorf("clear capabilities: %w", err)
	}
	return nil
}
