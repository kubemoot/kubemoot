package confine

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// workspaceNames are directory names that a workspace volume is mounted under.
var workspaceNames = map[string]bool{"workspace": true, "artifacts": true}

// UnlistedWorkspaceMounts are the mount points in mountinfo whose name is that of a workspace volume
// (workspace or artifacts) but that are not among listed, so a program would see them. The buildpacks
// application directory, /workspace, is not a volume and is ignored.
func UnlistedWorkspaceMounts(mountinfo io.Reader, listed []string) []string {
	known := map[string]bool{"/workspace": true}
	for _, dir := range listed {
		known[filepath.Clean(dir)] = true
	}
	var out []string
	sc := bufio.NewScanner(mountinfo)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		const mountPointField = 4
		if len(fields) <= mountPointField {
			continue
		}
		point := filepath.Clean(unescape(fields[mountPointField]))
		if !known[point] && workspaceNames[filepath.Base(point)] {
			out = append(out, point)
		}
	}
	return out
}

// UnlistedWorkspaceMountsOfHost reads this process's own mounts.
func UnlistedWorkspaceMountsOfHost(listed []string) []string {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	return UnlistedWorkspaceMounts(f, listed)
}

// unescape reads the octal escapes (\040 for a space) mountinfo writes into a path.
func unescape(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}
