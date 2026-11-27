package inputs

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// ErrLink is the refusal of a path that has a symbolic link in it.
var ErrLink = errors.New("a symbolic link is in its path")

// openDirFlags and openFileFlags never follow a link. A directory is opened with O_PATH-free flags so
// the next component is opened relative to it: the check and the open are one operation, so a link
// planted between a check and a later open cannot be followed.
const (
	openDirFlags  = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	openFileFlags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
)

// OpenFile opens the regular file the parts name under root and returns it open, so what is read is
// what was checked. Every component is opened relative to the directory before it with O_NOFOLLOW, so a
// symbolic link anywhere in the path is refused with ErrLink. A missing path wraps os.ErrNotExist, and
// anything that is not a regular file is reported as missing.
func OpenFile(root string, parts []string) (*os.File, error) {
	dir, err := unix.Open(root, openDirFlags, 0)
	if err != nil {
		return nil, linkOrErr(err, root)
	}
	for i, part := range parts {
		last := i == len(parts)-1
		flags := openDirFlags
		if last {
			flags = openFileFlags
		}
		fd, err := openPart(dir, part, flags)
		if err != nil {
			return nil, err
		}
		dir = fd
	}
	f := os.NewFile(uintptr(dir), strings.Join(parts, "/"))
	var st unix.Stat_t
	if err := unix.Fstat(dir, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = f.Close()
		return nil, fmt.Errorf("it is not a regular file: %w", os.ErrNotExist)
	}
	return f, nil
}

// CheckDir reports whether the parts name a directory under root with no symbolic link in the path.
func CheckDir(root string, parts []string) error {
	dir, err := unix.Open(root, openDirFlags, 0)
	if err != nil {
		return linkOrErr(err, root)
	}
	for _, part := range parts {
		fd, err := openPart(dir, part, openDirFlags)
		if err != nil {
			return err
		}
		dir = fd
	}
	return unix.Close(dir)
}

// openPart opens one component relative to the directory dir and closes dir, whether or not it succeeds.
// A link is told from a file by what the directory entry is: a link opened with O_DIRECTORY|O_NOFOLLOW
// fails with ENOTDIR, the same as a plain file does.
func openPart(dir int, part string, flags int) (int, error) {
	defer func() { _ = unix.Close(dir) }()
	fd, err := unix.Openat(dir, part, flags, 0)
	if err == nil {
		return fd, nil
	}
	var st unix.Stat_t
	if unix.Fstatat(dir, part, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return -1, ErrLink
	}
	return -1, linkOrErr(err, part)
}

// linkOrErr maps the errors of opening one path component: ELOOP is a link, a file where a directory
// was needed is a missing path.
func linkOrErr(err error, part string) error {
	switch {
	case errors.Is(err, unix.ELOOP):
		return ErrLink
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
		return fmt.Errorf("%s: %w", part, os.ErrNotExist)
	}
	return fmt.Errorf("%s: %w", part, err)
}
