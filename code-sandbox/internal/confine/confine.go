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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/kubemoot/kubemoot/code-sandbox/internal/inputs"
)

// Arg is the first argument of a re-executed sandbox binary that should confine and then run a program.
const Arg = "--code-sandbox-confine"

const (
	flagMount    = "--mount"
	flagPrivate  = "--private"
	flagTempSize = "--tmpsize"
	flagRequire  = "--require-proc"
	flagStatus   = "--status-fd"
	flagEnd      = "--"
	// noView is the descriptor number of a Mount that shows an empty directory.
	noView = -1
	// exitConfine is the exit code of a helper that could not set the view up.
	exitConfine = 125
	// FailurePrefix starts the message a helper writes to stderr when it could not set the view up.
	FailurePrefix = "code-sandbox: the workspace could not be hidden from the program"
	// ExitCode is the exit code of a helper that could not set the view up.
	ExitCode = exitConfine
	// ProcNotReplaced starts the report the helper writes to the status descriptor when /proc is not replaced.
	ProcNotReplaced = "proc-not-replaced"
	// capLimit bounds the capability numbers tried when the bounding set is emptied.
	capLimit = 64
)

// sizePattern is a tmpfs size: digits and an optional unit, so the value cannot add mount options.
var sizePattern = regexp.MustCompile(`^[0-9]+[kKmMgG]?$`)

// ValidSize reports whether s is a size the private temporary directory accepts, like 256m.
func ValidSize(s string) bool { return sizePattern.MatchString(s) }

// Mount replaces the workspace directory Path with its folder Thread, "ns/crew/thread" (empty for none).
// The helper opens that folder itself, in its own mount namespace, one component at a time without
// following a link, and mounts the open directory, so a link planted at any moment cannot change what is
// shown: a folder that is, or sits behind, a link, or is missing, shows an empty directory. (A descriptor
// the parent opened cannot be mounted: it belongs to the parent's mount namespace.)
type Mount struct {
	Path   string
	Thread string
}

// NoFD is the status descriptor of a run that has none.
const NoFD = noView

// Spec is everything the helper needs before it runs the program.
type Spec struct {
	Mounts []Mount
	// RunDir is the helper's working directory; it becomes the only entry of its parent, which is
	// replaced by a private temporary directory, so concurrent programs cannot see each other's files.
	RunDir string
	// TempSize is the size of that temporary directory, like 256m.
	TempSize string
	// RequireProc makes a /proc that cannot be replaced a failure instead of a report.
	RequireProc bool
	// StatusFD is the descriptor the helper reports to, or NoView for none.
	StatusFD int
}

// Command is the argument list that starts argv confined by spec, run through the binary self.
func Command(self string, spec Spec, argv []string) []string {
	out := []string{self, Arg, flagTempSize, spec.TempSize, flagPrivate, spec.RunDir,
		flagStatus, strconv.Itoa(spec.StatusFD)}
	if spec.RequireProc {
		out = append(out, flagRequire)
	}
	for _, m := range spec.Mounts {
		out = append(out, flagMount, m.Path, m.Thread)
	}
	out = append(out, flagEnd)
	return append(out, argv...)
}

// Attr gives the helper process its own user, mount and pid namespace. The current user and group map to
// themselves, so files the program works on keep their owner. Two capabilities stay ambient until the
// helper has set the view up and dropped them.
func Attr() *syscall.SysProcAttr {
	uid, gid := os.Getuid(), os.Getgid()
	return &syscall.SysProcAttr{
		Setpgid:                    true,
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID,
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
	spec, argv, err := parse(args)
	if err != nil {
		return err
	}
	status := statusFile(spec.StatusFD)
	if err := apply(spec, status); err != nil {
		return err
	}
	closeStatus(status)
	if err := dropCapabilities(); err != nil {
		return err
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}

func statusFile(fd int) *os.File {
	if fd == noView {
		return nil
	}
	return os.NewFile(uintptr(fd), "status")
}

// closeStatus closes the descriptor the parent passed, through its one *os.File, so the program
// inherits none of it and no finalizer closes the same number again later.
func closeStatus(status *os.File) {
	if status != nil {
		_ = status.Close()
	}
}

// parse reads what Command wrote, after the Arg itself.
func parse(args []string) (Spec, []string, error) {
	spec, args, err := parseHead(args)
	if err != nil {
		return spec, nil, err
	}
	spec.Mounts, args, err = parseMounts(args)
	if err != nil {
		return spec, nil, err
	}
	if len(args) < 2 || args[0] != flagEnd {
		return spec, nil, errors.New("no program to run")
	}
	return spec, args[1:], nil
}

// parseHead reads the temporary directory size, run directory, status descriptor and the proc requirement.
func parseHead(args []string) (Spec, []string, error) {
	spec := Spec{StatusFD: noView}
	if len(args) < 6 || args[0] != flagTempSize || args[2] != flagPrivate || args[4] != flagStatus {
		return spec, nil, errors.New("no run directory")
	}
	spec.TempSize, spec.RunDir = args[1], args[3]
	if !sizePattern.MatchString(spec.TempSize) {
		return spec, nil, fmt.Errorf("temporary directory size %q is not a number with an optional k, m or g", spec.TempSize)
	}
	fd, err := strconv.Atoi(args[5])
	if err != nil {
		return spec, nil, fmt.Errorf("status descriptor %q: %w", args[5], err)
	}
	spec.StatusFD, args = fd, args[6:]
	if len(args) > 0 && args[0] == flagRequire {
		spec.RequireProc, args = true, args[1:]
	}
	return spec, args, nil
}

// parseMounts reads the --mount groups at the start of args and returns them with the rest.
func parseMounts(args []string) ([]Mount, []string, error) {
	var mounts []Mount
	for len(args) > 0 && args[0] == flagMount {
		if len(args) < 3 {
			return nil, nil, errors.New("incomplete mount")
		}
		mounts = append(mounts, Mount{Path: args[1], Thread: args[2]})
		args = args[3:]
	}
	return mounts, args, nil
}

func apply(spec Spec, status *os.File) error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make the mounts private: %w", err)
	}
	runFD, err := unix.Open(".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open the run directory: %w", err)
	}
	defer func() { _ = unix.Close(runFD) }()
	for _, m := range spec.Mounts {
		if err := show(m, runFD); err != nil {
			return err
		}
	}
	if err := unix.Fchdir(runFD); err != nil {
		return err
	}
	if err := privateTemp(spec.RunDir, spec.TempSize); err != nil {
		return err
	}
	var report io.Writer
	if status != nil {
		report = status
	}
	return freshProc(spec.RequireProc, report)
}

// freshProc mounts a /proc that lists only the processes of the program's own pid namespace, so the
// server and other runs are not named there at all. When the host refuses the mount the old /proc stays,
// where the kernel's ptrace rules still deny every entry of a process in another user namespace. That is
// reported on status (the server logs it), or is a failure when require is set.
func freshProc(require bool, status io.Writer) error {
	err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "")
	switch {
	case err == nil:
		return nil
	case require:
		return fmt.Errorf("replace /proc: %w", err)
	}
	if status != nil {
		_, _ = fmt.Fprintf(status, "%s: %v\n", ProcNotReplaced, err)
	}
	return nil
}

// privateTemp replaces the run directory's parent with an empty temporary directory that holds only the
// run directory. The helper's working directory is the run directory, and "." still names it after the
// parent is covered, so it is the source of the bind.
func privateTemp(runDir, size string) error {
	parent := filepath.Dir(runDir)
	if !filepath.IsAbs(runDir) || parent == "/" || parent == "." {
		return fmt.Errorf("run directory %q has no private parent", runDir)
	}
	opts := "size=" + size + ",mode=1777"
	if err := unix.Mount("tmpfs", parent, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, opts); err != nil {
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

// show replaces m.Path with the thread's folder, opened here without following a link and entered with
// fchdir, so no path is resolved a second time, and makes it read-only. The run directory descriptor runFD
// returns the helper to where it was. Anything but a plain folder shows an empty directory.
func show(m Mount, runFD int) error {
	dir, err := openThread(m)
	if err != nil {
		if err := unix.Mount("tmpfs", m.Path, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=4k,mode=0555"); err != nil {
			return fmt.Errorf("hide %s: %w", m.Path, err)
		}
		return nil
	}
	defer func() { _ = dir.Close() }()
	if err := unix.Fchdir(int(dir.Fd())); err != nil {
		return fmt.Errorf("enter the folder of %s: %w", m.Path, err)
	}
	if err := unix.Mount(".", m.Path, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("show the folder at %s: %w", m.Path, err)
	}
	if err := unix.Fchdir(runFD); err != nil {
		return err
	}
	return readOnly(m.Path)
}

// openThread opens the folder of m, or fails when there is none, it is not plain, or the thread is not a path.
func openThread(m Mount) (*os.File, error) {
	if m.Thread == "" || !filepath.IsLocal(m.Thread) {
		return nil, errors.New("no folder")
	}
	return inputs.OpenDir(m.Path, strings.Split(strings.TrimSuffix(m.Thread, "/"), "/"))
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
