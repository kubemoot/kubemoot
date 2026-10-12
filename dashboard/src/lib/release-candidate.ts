// A release candidate's version is X.Y.Z-rc.N, the form the release pipeline stamps.
const RELEASE_CANDIDATE = /^v?\d+\.\d+\.\d+-rc\.\d+$/;

/** True when the version names a release candidate; false for a final or malformed version. */
export function isReleaseCandidate(version: string | null | undefined): boolean {
	return RELEASE_CANDIDATE.test(version ?? '');
}
