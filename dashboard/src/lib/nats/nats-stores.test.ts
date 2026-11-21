// @vitest-environment node
// The definition is the one source for the NATS page. These tests keep the two other
// places that list the stores, the Data ownership page and the operator chart's bootstrap
// job, from drifting away from it. They read files outside dashboard/, so they run where
// the whole repository is checked out and skip inside the image build, whose context is
// the dashboard directory alone.
import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { STORES, crewKeyFor, crewKeyedStores, jetstreamName, storeForStream, type PeoplesData } from './stores';
import { storeRow } from './view';
import type { StoreStatus } from './status';

const repo = (path: string) => fileURLToPath(new URL(`../../../../${path}`, import.meta.url));
const DOCS = repo('docs/operating/data-ownership.md');
const JOB = repo('operator/chart/kubemoot-operator/templates/nats-streams-job.yaml');
const haveRepo = existsSync(DOCS) && existsSync(JOB);

/** Rows of the markdown tables: the first cell's backticked name, and all cells. */
function docsRows(): { name: string; cells: string[] }[] {
	const text = readFileSync(DOCS, 'utf8');
	const section = text.slice(text.indexOf('## Every store Kubemoot creates'), text.indexOf('A limit marked'));
	return section
		.split('\n')
		.filter((l) => l.startsWith('| `'))
		.map((l) => {
			const cells = l.split('|').slice(1, -1).map((c) => c.trim());
			return { name: cells[0].replaceAll('`', ''), cells };
		});
}

function docsPeoplesData(cell: string): PeoplesData {
	if (cell.startsWith('Yes, derived')) return 'derived';
	if (cell.startsWith('Yes')) return 'yes';
	return cell.startsWith('No') ? 'no' : 'maybe';
}

const MIB = 1024 * 1024;

/** Store name to size limit in bytes from the bootstrap job's literal limits. */
function jobLimits(): Map<string, number> {
	const out = new Map<string, number>();
	const size = (s: string) => Number.parseInt(s, 10) * MIB * (s.endsWith('GiB') ? 1024 : 1);
	for (const line of readFileSync(JOB, 'utf8').split('\n')) {
		const name = /(?:stream_limits|kv_bucket) (\w+)/.exec(line)?.[1];
		const limit = /--max-(?:bytes|bucket-size)=(\d+[MG]iB)/.exec(line)?.[1];
		if (name && limit) out.set(name, size(limit));
	}
	return out;
}

describe('the shared definition', () => {
	it('names every store once, with the fields the page needs', () => {
		const names = STORES.map((s) => s.name);
		expect(new Set(names).size).toBe(names.length);
		for (const s of STORES) {
			expect(['stream', 'kv', 'object']).toContain(s.kind);
			expect(['crew', 'system-learning', 'kubemoot']).toContain(s.owner);
			expect(['yes', 'derived', 'maybe', 'no']).toContain(s.peoplesData);
			expect(s.holds.length).toBeGreaterThan(10);
		}
	});

	it('finds a store from the stream that holds it', () => {
		expect(storeForStream('KUBEMOOT_DISCUSS')?.name).toBe('KUBEMOOT_DISCUSS');
		expect(storeForStream('KV_kubemoot_crew_memory')?.kind).toBe('kv');
		expect(storeForStream('OBJ_kubemoot_workspace')?.kind).toBe('object');
	});

	it('does not take a name of the wrong kind or an unknown one', () => {
		expect(storeForStream('KV_KUBEMOOT_DISCUSS')).toBeUndefined();
		expect(storeForStream('OBJ_kubemoot_crew_memory')).toBeUndefined();
		expect(storeForStream('somebody_elses_stream')).toBeUndefined();
	});

	it('names the stream behind a KV bucket and an object store', () => {
		expect(jetstreamName({ name: 'kubemoot_conversations', kind: 'kv' })).toBe('KV_kubemoot_conversations');
		expect(jetstreamName({ name: 'kubemoot_workspace', kind: 'object' })).toBe('OBJ_kubemoot_workspace');
		expect(jetstreamName({ name: 'KUBEMOOT_CHAT', kind: 'stream' })).toBe('KUBEMOOT_CHAT');
	});

	it('fills a crew key from a namespace and a crew', () => {
		expect(crewKeyFor('kubemoot.discuss.{ns}.{crew}.>', 'team-a', 'pilot')).toBe('kubemoot.discuss.team-a.pilot.>');
		expect(crewKeyedStores().every((s) => s.crewKey?.includes('{ns}') && s.crewKey.includes('{crew}'))).toBe(true);
	});

	it('is what the page row shows for a store', () => {
		const status = {
			definition: STORES.find((s) => s.name === 'kubemoot_crew_memory') ?? null,
			name: 'kubemoot_crew_memory',
			kind: 'kv',
			limits: { maxAgeSeconds: null, maxBytes: 100, maxMessages: null, history: 5 },
			usage: { messages: 10, bytes: 50, firstSeq: 1, lastSeq: 10, consumers: 0, keys: 2 }
		} as StoreStatus;
		const row = storeRow(status);
		expect(row.peoplesData).toBe('Yes, derived');
		expect(row.owner).toBe('A crew');
		expect(row.holds).toContain('Facts a crew learned');
		expect(row.bytesShare).toBe('50%');
	});

	it('marks a store that is not in the definition', () => {
		const row = storeRow({
			definition: null,
			name: 'other',
			kind: 'stream',
			limits: { maxAgeSeconds: null, maxBytes: null, maxMessages: null, history: null },
			usage: { messages: 0, bytes: 0, firstSeq: 0, lastSeq: 0, consumers: 0, keys: null }
		});
		expect(row.unknown).toBe(true);
		expect(row.sequences).toBe('empty');
		expect(row.maxBytes).toBe('unlimited');
	});
});

describe.skipIf(!haveRepo)('the definition against the docs and the bootstrap job', () => {
	it('lists every store of the Data ownership tables, and only those', () => {
		const byName = (a: string, b: string) => a.localeCompare(b);
		const docs = docsRows().map((r) => r.name).sort(byName);
		expect(STORES.map((s) => s.name).sort(byName)).toEqual(docs);
	});

	it('agrees with the docs on whether a store can hold people\'s data', () => {
		for (const row of docsRows()) {
			const store = STORES.find((s) => s.name === row.name);
			expect(store?.peoplesData, row.name).toBe(docsPeoplesData(row.cells[2]));
		}
	});

	it('creates every store the bootstrap job creates, and the job creates every store', () => {
		const job = readFileSync(JOB, 'utf8');
		const created = [...job.matchAll(/(?:stream_limits|kv_bucket|object_store) (\w+)/g)].map((m) => m[1]);
		const known = new Set(STORES.map((s) => s.name));
		expect(created.filter((n) => !known.has(n))).toEqual([]);
		for (const s of STORES) expect(created, s.name).toContain(s.name);
	});

	it('gives each store the size limit the bootstrap job sets', () => {
		const limits = jobLimits();
		for (const s of STORES.filter((x) => x.defaultMaxBytes !== null)) {
			expect(limits.get(s.name), s.name).toBe(s.defaultMaxBytes);
		}
	});
});
