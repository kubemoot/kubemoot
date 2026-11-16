import { describe, expect, it, vi } from 'vitest';

const nats = vi.hoisted(() => ({ getNatsConnection: vi.fn() }));
vi.mock('./nats-client.js', () => nats);

import {
	DISCUSSION_ARTIFACTS_BUCKET,
	DISCUSSION_READ_BUCKETS,
	DISCUSSION_WORKSPACE_BUCKET,
	NatsBucketMissing,
	firstBlob,
	readDiscussionArtifact,
	isStreamNotFound
} from './nats-object-store.js';

const bytes = (s: string) => new TextEncoder().encode(s);

describe('firstBlob', () => {
	it('names the two buckets as the other components do (checked across components by .github/scripts/test-bucket-names.sh)', () => {
		expect(DISCUSSION_WORKSPACE_BUCKET).toBe('kubemoot_workspace');
		expect(DISCUSSION_ARTIFACTS_BUCKET).toBe('kubemoot_discussion_artifacts');
	});

	it('reads the workspace bucket before the older bucket', () => {
		expect(DISCUSSION_READ_BUCKETS).toEqual([DISCUSSION_WORKSPACE_BUCKET, DISCUSSION_ARTIFACTS_BUCKET]);
	});

	it('answers from the first bucket that holds the object', async () => {
		const open = vi.fn(async (b: string) => bytes(`from ${b}`));
		const got = await firstBlob(DISCUSSION_READ_BUCKETS, open);
		expect(new TextDecoder().decode(got!)).toBe('from kubemoot_workspace');
		expect(open).toHaveBeenCalledTimes(1);
	});

	it('falls through to the older bucket when the object is absent from the first', async () => {
		const got = await firstBlob(DISCUSSION_READ_BUCKETS, async (b) => (b === DISCUSSION_ARTIFACTS_BUCKET ? bytes('old') : null));
		expect(new TextDecoder().decode(got!)).toBe('old');
	});

	it('falls through when the first bucket does not exist yet', async () => {
		const got = await firstBlob(DISCUSSION_READ_BUCKETS, async (b) => {
			if (b === DISCUSSION_WORKSPACE_BUCKET) throw new NatsBucketMissing(b);
			return bytes('old');
		});
		expect(new TextDecoder().decode(got!)).toBe('old');
	});

	it('returns null when no bucket holds the object', async () => {
		expect(await firstBlob(DISCUSSION_READ_BUCKETS, async () => null)).toBeNull();
	});

	it('throws any other failure, so NATS being down is not read as a missing object', async () => {
		await expect(
			firstBlob(DISCUSSION_READ_BUCKETS, async () => {
				throw new Error('connection closed');
			})
		).rejects.toThrow('connection closed');
	});
});

describe('isStreamNotFound', () => {
	it('is true only for the stream-not-found error', () => {
		expect(isStreamNotFound(new Error('stream not found'))).toBe(true);
		expect(isStreamNotFound(new Error('connection closed'))).toBe(false);
		expect(isStreamNotFound('stream not found')).toBe(false);
	});
});

describe('readDiscussionArtifact', () => {
	function connection(existing: string[], blobs: Record<string, Uint8Array | null>) {
		const os = vi.fn(async (bucket: string) => ({ getBlob: async () => blobs[bucket] ?? null }));
		nats.getNatsConnection.mockResolvedValue({
			jetstream: () => ({ views: { os } }),
			jetstreamManager: async () => ({
				streams: {
					info: async (stream: string) => {
						if (!existing.includes(stream.replace('OBJ_', ''))) throw new Error('stream not found');
						return {};
					}
				}
			})
		});
		return os;
	}

	it('never opens (and so never creates) a bucket that does not exist', async () => {
		const os = connection([DISCUSSION_ARTIFACTS_BUCKET], { [DISCUSSION_ARTIFACTS_BUCKET]: bytes('old') });
		expect(new TextDecoder().decode((await readDiscussionArtifact('k'))!)).toBe('old');
		expect(os).toHaveBeenCalledTimes(1);
		expect(os).toHaveBeenCalledWith(DISCUSSION_ARTIFACTS_BUCKET);
	});

	it('does not create the older bucket either when it is missing too', async () => {
		const os = connection([], {});
		expect(await readDiscussionArtifact('k')).toBeNull();
		expect(os).not.toHaveBeenCalled();
	});
});
