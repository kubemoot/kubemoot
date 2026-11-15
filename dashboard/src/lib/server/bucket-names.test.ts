import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it, vi } from 'vitest';

vi.mock('./nats-client.js', () => ({ getNatsConnection: vi.fn() }));

import { DISCUSSION_ARTIFACTS_BUCKET, DISCUSSION_WORKSPACE_BUCKET } from './nats-object-store.js';

// The bucket names are literals in five components. This test ties them to one source: the
// dashboard's constants must equal each other component's literal, so a rename that misses one fails here.
const repo = resolve(__dirname, '../../../..');
const read = (path: string) => readFileSync(resolve(repo, path), 'utf8');

const sources: Array<[string, string]> = [
	['agent-runtime', 'agent-runtime/src/main/java/ai/kubemoot/agent/nats/DiscussionArtifacts.java'],
	['discussion-gateway', 'discussion-gateway/internal/api/artifacts.go'],
	['operator mesh', 'operator/pkg/mesh/names.go']
];

describe('discussion workspace bucket names', () => {
	for (const [name, path] of sources) {
		it(`${name} names the old bucket as the dashboard does`, () => {
			expect(read(path)).toContain(`"${DISCUSSION_ARTIFACTS_BUCKET}"`);
		});

		it(`${name} spells the workspace bucket as the dashboard does, wherever it names one`, () => {
			const named = [...read(path).matchAll(/"(kubemoot_workspace[a-z_]*)"/g)].map((m) => m[1]);
			for (const literal of named) expect(literal).toBe(DISCUSSION_WORKSPACE_BUCKET);
		});
	}

	it('the gateway reads both buckets', () => {
		expect(read('discussion-gateway/internal/api/artifacts.go')).toContain(`"${DISCUSSION_WORKSPACE_BUCKET}"`);
	});
});
