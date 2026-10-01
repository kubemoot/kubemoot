// NATS Object Store reader for fitness-suite XLSX artifacts.
//
// The operator writes XLSX bytes to bucket `kubemoot_fitness_artifacts`
// at key `{namespace}/{suite-name}/{runId}.xlsx`. The dashboard download
// handler reads them back via this helper. Connection is shared via
// the existing nats-client.ts singleton.

import { getNatsConnection } from './nats-client.js';
import { compareCodeUnits } from '$lib/text-utils';

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
 * Throws when NATS itself is unreachable — caller surfaces this as a
 * 5xx so the failure mode is distinguishable from "artifact missing"
 * (which is a 404).
 */
export async function readFitnessArtifact(objectKey: string): Promise<Uint8Array | null> {
	const nc = await getNatsConnection();
	const js = nc.jetstream();
	// views.os is idempotent — returns an existing bucket or creates one.
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
export async function readFitnessTranscript(objectKey: string): Promise<unknown | null> {
	const bytes = await readFitnessArtifact(objectKey);
	if (!bytes) return null;
	return JSON.parse(new TextDecoder().decode(bytes));
}

// DISCUSSION_ARTIFACTS_BUCKET must match the agent-runtime's constant
// (DiscussionArtifacts.BUCKET in kubemoot/agent-runtime/src/main/java/ai/
// kubemoot/agent/nats/DiscussionArtifacts.java). The spill in DiscussionSubscriber
// writes a large agent contribution here at key
// `{namespace}/{crew}/{threadId}/{agent}/{signal}-{uuid}` (see $lib/crewScope)
// and appends an `[ARTIFACT key=...]` marker to the inline message. The two literals
// are kept in sync by code review.
export const DISCUSSION_ARTIFACTS_BUCKET = 'kubemoot_discussion_artifacts';

/**
 * Read a spilled discussion artifact (the FULL contribution behind an
 * `[ARTIFACT key=...]` marker) from the discussion-artifacts bucket. Returns the
 * bytes as a Uint8Array, or null when the object is absent (TTL-pruned, GC-reaped,
 * or never written). Mirrors readFitnessArtifact but against the discussion bucket.
 *
 * Throws when NATS itself is unreachable so the caller can distinguish a 5xx
 * (NATS down) from a 404 (artifact missing).
 */
export async function readDiscussionArtifact(objectKey: string): Promise<Uint8Array | null> {
	const nc = await getNatsConnection();
	const js = nc.jetstream();
	const store = await js.views.os(DISCUSSION_ARTIFACTS_BUCKET);
	return await store.getBlob(objectKey);
}
