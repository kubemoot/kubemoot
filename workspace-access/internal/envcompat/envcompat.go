// Package envcompat reads the component's WORKSPACE_* environment variables and falls back to
// the ARTIFACT_* name each one had before the discussion's artifacts became its workspace. The
// old names keep working for one release; using one logs a warning, once per variable.
package envcompat

import (
	"strings"
	"sync"
)

const (
	currentPrefix = "WORKSPACE_"
	legacyPrefix  = "ARTIFACT_"
)

// Wrap returns a lookup that answers a WORKSPACE_X name from that variable, or, when it is
// empty, from ARTIFACT_X. warn is called once per legacy variable that supplied a value. A name
// that does not start with WORKSPACE_ is passed through.
func Wrap(getenv func(string) string, warn func(format string, args ...any)) func(string) string {
	var warned sync.Map
	return func(name string) string {
		v := getenv(name)
		rest, isCurrent := strings.CutPrefix(name, currentPrefix)
		if v != "" || !isCurrent {
			return v
		}
		legacy := legacyPrefix + rest
		v = getenv(legacy)
		if v != "" {
			if _, seen := warned.LoadOrStore(legacy, true); !seen {
				warn("%s is deprecated and is removed in the next release; set %s instead", legacy, name)
			}
		}
		return v
	}
}

// The two buckets of the discussion workspace. Writers use the new one; readers read both, the
// new bucket first, so a discussion that spans the change keeps its calls until the old bucket's
// TTL drains it.
const (
	// Bucket is the workspace bucket.
	Bucket = "kubemoot_workspace"
	// LegacyBucket is the bucket the workspace was stored in before it had its name.
	LegacyBucket = "kubemoot_discussion_artifacts"
)

// Buckets are the buckets a reader tries, in order: WORKSPACE_BUCKET or the workspace bucket,
// then ARTIFACT_BUCKET or the legacy bucket, with a bucket named twice kept once. raw reads the
// environment without the ARTIFACT_ fallback, so the two variables stay separate.
func Buckets(raw func(string) string, warn func(format string, args ...any)) []string {
	current := orDefault(raw(currentPrefix+"BUCKET"), Bucket)
	legacy := orDefault(raw(legacyPrefix+"BUCKET"), LegacyBucket)
	if raw(legacyPrefix+"BUCKET") != "" && raw(currentPrefix+"BUCKET") == "" {
		warn("%sBUCKET is deprecated and is removed in the next release; set %sBUCKET instead", legacyPrefix, currentPrefix)
	}
	if current == legacy {
		return []string{current}
	}
	return []string{current, legacy}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
