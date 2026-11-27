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
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// Arg is the first argument of a re-executed sandbox binary that should confine and then run a program.
const Arg = "--code-sandbox-confine"

const (
	flagMount   = "--mount"
	flagPrivate = "--private"
	flagEnd     = "--"
	// exitConfine is the exit code of a helper that could not set the view up.
	exitConfine = 125
	// FailurePrefix starts the message a helper writes to stderr when it could not set the view up.
	FailurePrefix = "code-sandbox: the workspace could not be hidden from the program"
	// ExitCode is the exit code of a helper that could not set the view up.
	ExitCode = exitConfine
	// tempSize limits the private temporary directory a program gets.
	tempSize = "size=256m,mode=1777"
	// capLimit bounds the capability numbers tried when the bounding set is emptied.
	capLimit = 64
)

// Mount replaces the directory Path with View for the program; an empty View shows an empty directory.
type Mount struct {
	Path string
	View string
}

// Command is the argument list that starts argv confined by mounts, run through the binary self.
// The run directory (the helper's working directory) becomes the only entry of its parent, which is
// replaced by a private temporary directory, so concurrent programs cannot see each other's files.
func Command(self string, mounts []Mount, runDir string, argv []string) []string {
	out := []string{self, Arg, flagPrivate, runDir}
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
		fmt.Fprintf(os.Stderr, "%s: %v\n", FailurePrefix, err)
		os.Exit(exitConfine)
	}
}

func run(args []string) error {
	mounts, runDir, argv, err := parse(args)
	if err != nil {
		return err
	}
	if err := apply(mounts, runDir); err != nil {
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
func parse(args []string) (mounts []Mount, runDir string, argv []string, err error) {
	if len(args) < 2 || args[0] != flagPrivate {
		return nil, "", nil, errors.New("no run directory")
	}
	runDir, args = args[1], args[2:]
	for len(args) > 0 && args[0] == flagMount {
		if len(args) < 3 {
			return nil, "", nil, errors.New("incomplete mount")
		}
		mounts = append(mounts, Mount{Path: args[1], View: args[2]})
		args = args[3:]
	}
	if len(args) < 2 || args[0] != flagEnd {
		return nil, "", nil, errors.New("no program to run")
	}
	return mounts, runDir, args[1:], nil
}

func apply(mounts []Mount, runDir string) error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make the mounts private: %w", err)
	}
	for _, m := range mounts {
		if err := show(m); err != nil {
			return err
		}
	}
	return privateTemp(runDir)
}

// privateTemp replaces the run directory's parent with an empty temporary directory that holds only the
// run directory. The helper's working directory is the run directory, and "." still names it after the
// parent is covered, so it is the source of the bind.
func privateTemp(runDir string) error {
	parent := filepath.Dir(runDir)
	if !filepath.IsAbs(runDir) || parent == "/" || parent == "." {
		return fmt.Errorf("run directory %q has no private parent", runDir)
	}
	if err := unix.Mount("tmpfs", parent, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, tempSize); err != nil {
		return fmt.Errorf("private %s: %w", parent, err)
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		return err
	}
	if err := unix.Mount(".", runDir, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind %s: %w", runDir, err)
	}
	return os.Chdir(runDir)
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
	return readOnly(m.Path)
}

// lockedFlags are the mount flags a remount in a user namespace must keep as the mount has them.
const lockedFlags = unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC |
	unix.MS_NOATIME | unix.MS_NODIRATIME | unix.MS_RELATIME

// readOnly remounts a bind mount read-only, so a program cannot plant a link or change a stored result
// in the discussion's folder. The flags the mount already has stay.
func readOnly(path string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	flags := uintptr(st.Flags)&lockedFlags | unix.MS_BIND | unix.MS_REMOUNT | unix.MS_RDONLY |
		unix.MS_NOSUID | unix.MS_NODEV
	if err := unix.Mount("", path, "", flags, ""); err != nil {
		return fmt.Errorf("make %s read-only: %w", path, err)
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
