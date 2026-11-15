import { describe, expect, it, vi } from 'vitest';

vi.mock('./nats-client.js', () => ({ getNatsConnection: vi.fn() }));

import {
	DISCUSSION_ARTIFACTS_BUCKET,
	DISCUSSION_READ_BUCKETS,
	DISCUSSION_WORKSPACE_BUCKET,
	NatsBucketMissing,
	firstBlob,
	isStreamNotFound
} from './nats-object-store.js';

const bytes = (s: string) => new TextEncoder().encode(s);

describe('firstBlob', () => {
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
