// Package gomem sets GOMEMLIMIT on the Go containers the operator builds, from the memory limit
// each one runs under, so the Go garbage collector works harder near the limit instead of the
// kernel killing the container.
package gomem

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"
)

// EnvName is the variable the Go runtime reads.
const EnvName = "GOMEMLIMIT"

// Percent is the share of the memory limit given to GOMEMLIMIT. The rest is room for memory
// outside the Go heap and runtime, which GOMEMLIMIT does not cover.
const Percent = 90

const bytesPerMiB = 1 << 20

// Env returns GOMEMLIMIT for a container with these resources: Percent of its memory limit, in
// whole MiB. It returns false when there is no memory limit or the limit is under 1 MiB, so a
// container without a limit gets none rather than a wrong one.
func Env(res corev1.ResourceRequirements) (corev1.EnvVar, bool) {
	limit, ok := res.Limits[corev1.ResourceMemory]
	if !ok {
		return corev1.EnvVar{}, false
	}
	mib := limit.Value() / bytesPerMiB * Percent / 100
	if mib <= 0 {
		return corev1.EnvVar{}, false
	}
	return corev1.EnvVar{Name: EnvName, Value: strconv.FormatInt(mib, 10) + "MiB"}, true
}

// With returns env with GOMEMLIMIT added from res. A GOMEMLIMIT already in env is kept, because
// the user set it. When res has no usable limit it returns env unchanged.
func With(env []corev1.EnvVar, res corev1.ResourceRequirements) []corev1.EnvVar {
	v, ok := Env(res)
	if !ok {
		return env
	}
	for _, e := range env {
		if e.Name == EnvName {
			return env
		}
	}
	return append(env, v)
}
