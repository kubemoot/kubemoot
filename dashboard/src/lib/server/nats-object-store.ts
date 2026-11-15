// NATS Object Store reader for fitness-suite XLSX artifacts.
//
// The operator writes XLSX bytes to bucket `kubemoot_fitness_artifacts`
// at key `{namespace}/{suite-name}/{runId}.xlsx`. The dashboard download
// handler reads them back via this helper. Connection is shared via
// the existing nats-client.ts singleton.

import { getNatsConnection } from './nats-client.js';
import { compareCodeUnits } from '#lib/text-utils.js';

// FITNESS_ARTIFACTS_BUCKET must match the operator's constant
// (FitnessArtifactsBucket in kubemoot/operator/internal/controller/
// crewfitnesssuite_controller.go). The two literals are kept in sync
// by code review.
export const FITNESS_ARTIFACTS_BUCKET = 'kubemoot_fitness_artifacts';

/**
 * Read an object from the fitness-artifacts bucket. Returns the bytes
 * as a Uint8Array, or null when the object is absent (operator hasn't
 * written it yet, or TTL has pruned it).
 *
 * Throws when NATS itself is unreachable - caller surfaces this as a
 * 5xx so the failure mode is distinguishable from "artifact missing"
 * (which is a 404).
 */
export async function readFitnessArtifact(objectKey: string): Promise<Uint8Array | null> {
	const nc = await getNatsConnection();
	const js = nc.jetstream();
	// views.os is idempotent - returns an existing bucket or creates one.
	// Dashboard "creating" the bucket is harmless: subsequent operator
	// writes share it.
	const store = await js.views.os(FITNESS_ARTIFACTS_BUCKET);
	return await store.getBlob(objectKey);
}

/**
 * List object keys in the fitness-artifacts bucket whose name starts with the
 * given prefix. Enumerates the per-iteration transcript blobs for a suite run
 * (prefix `{namespace}/{suite}/{runId}/`). Returns names only; the caller
 * fetches each transcript lazily via readFitnessTranscript.
 */
export async function listFitnessObjects(prefix: string): Promise<string[]> {
	const nc = await getNatsConnection();
	const js = nc.jetstream();
	const store = await js.views.os(FITNESS_ARTIFACTS_BUCKET);
	const entries = await store.list();
	return entries
		.filter((e) => !e.deleted && e.name.startsWith(prefix))
		.map((e) => e.name)
		.sort(compareCodeUnits);
}

/**
 * Read a per-iteration transcript JSON blob and parse it. Returns null when the
 * object is absent (TTL-pruned or never written). Shape mirrors the
 * fitness-runner's RunOutcome (assertions + events + run metadata).
 */
export async function readFitnessTranscript(objectKey: string): Promise<unknown> {
	const bytes = await readFitnessArtifact(objectKey);
	if (!bytes) return null;
	return JSON.parse(new TextDecoder().decode(bytes));
}

// The discussion workspace's two Object Store buckets. DISCUSSION_ARTIFACTS_BUCKET must match the
// agent-runtime's constant (DiscussionArtifacts.BUCKET in kubemoot/agent-runtime/src/main/java/ai/
// kubemoot/agent/nats/DiscussionArtifacts.java): the agent runtime still writes each tool result
// there at key `{namespace}/{crew}/{threadId}/{agent}/{nnn}-{tool}` (see #lib/crewScope).
// DISCUSSION_WORKSPACE_BUCKET is the same data under its new name; readers look in it first so they
// keep working when writers move. The literals are kept in sync by code review.
export const DISCUSSION_ARTIFACTS_BUCKET = 'kubemoot_discussion_artifacts';
export const DISCUSSION_WORKSPACE_BUCKET = 'kubemoot_workspace';

/** The buckets a stored result is read from, in order. */
export const DISCUSSION_READ_BUCKETS = [DISCUSSION_WORKSPACE_BUCKET, DISCUSSION_ARTIFACTS_BUCKET] as const;

/**
 * The object from the first bucket that holds it. A bucket that does not exist yet (no writer has
 * created it) counts as not holding it; any other failure is thrown so the caller can tell NATS being
 * down from the object being absent. `open` returns the bucket's bytes for the key, null when the
 * object is absent, and throws NatsBucketMissing when the bucket is.
 */
export class NatsBucketMissing extends Error {}

export async function firstBlob(
	buckets: readonly string[],
	open: (bucket: string) => Promise<Uint8Array | null>
): Promise<Uint8Array | null> {
	for (const bucket of buckets) {
		try {
			const bytes = await open(bucket);
			if (bytes) return bytes;
		} catch (e) {
			if (!(e instanceof NatsBucketMissing)) throw e;
		}
	}
	return null;
}

/**
 * Read a stored discussion artifact (a tool result file, or the full contribution behind an
 * `[ARTIFACT key=...]` marker in a thread from an earlier release) from the workspace bucket, then
 * the older discussion-artifacts bucket. Returns the bytes as a Uint8Array, or null when the object
 * is absent (TTL-pruned, GC-reaped, or never written). Mirrors readFitnessArtifact but against the
 * discussion buckets.
 *
 * Throws when NATS itself is unreachable so the caller can distinguish a 5xx
 * (NATS down) from a 404 (artifact missing).
 */
export async function readDiscussionArtifact(objectKey: string): Promise<Uint8Array | null> {
	const nc = await getNatsConnection();
	const js = nc.jetstream();
	return firstBlob(DISCUSSION_READ_BUCKETS, async (bucket) => {
		// views.os creates a bucket that is not there, with no TTL; the buckets are created by their
		// writers, so check each exists before opening it.
		if (!(await bucketExists(nc, bucket))) {
			throw new NatsBucketMissing(bucket);
		}
		const store = await js.views.os(bucket);
		return await store.getBlob(objectKey);
	});
}

/** True when the Object Store bucket exists; false only for NATS's "stream not found". */
async function bucketExists(nc: Awaited<ReturnType<typeof getNatsConnection>>, bucket: string): Promise<boolean> {
	const jsm = await nc.jetstreamManager();
	try {
		await jsm.streams.info(`OBJ_${bucket}`);
		return true;
	} catch (e) {
		if (isStreamNotFound(e)) return false;
		throw e;
	}
}

/** True for the error NATS gives when a stream does not exist. */
export function isStreamNotFound(e: unknown): boolean {
	return e instanceof Error && /stream not found/i.test(e.message);
}
