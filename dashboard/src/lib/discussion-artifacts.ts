// Shared client-side helpers for discussion/finding/synthesis rendering, used by
// BOTH the live discussions view (src/routes/discussions/+page.svelte) and the
// fitness transcript view (src/routes/fitness/+page.svelte). Kept in one place so
// the marker parse, the download URL, and the collapse threshold can never drift
// between the two views (the divergent-duplication smell this module exists to
// prevent).

import { resolve } from '$app/paths';

// A synthesis longer than this (chars) gets the collapse/expand affordance.
export const SYNTHESIS_COLLAPSE_CHARS = 600;

// Marker parsing for `[ARTIFACT key=... bytes=N - ...]`, which a thread stored by an earlier
// release holds in place of a large contribution. The key terminates at whitespace or `]`.
const ARTIFACT_KEY_RE = /\[ARTIFACT key=([^\]\s]+)/;

/** Extract the spilled-artifact key from a finding/synthesis body, or null when absent. */
export function artifactKey(text: string | undefined | null): string | null {
	if (!text) return null;
	const m = ARTIFACT_KEY_RE.exec(text);
	return m ? m[1] : null;
}

/** Read-only download endpoint for a spilled artifact (base-path aware). */
export function artifactHref(key: string): string {
	return `${resolve('/api/kubemoot/discussions/artifact')}?key=${encodeURIComponent(key)}`;
}
