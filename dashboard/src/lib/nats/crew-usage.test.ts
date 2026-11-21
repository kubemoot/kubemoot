import { describe, expect, it } from 'vitest';
import { CREW_ENTRY_CAP, formatCapped, kvSubjectFilter, usageFromObjects, usageFromSubjects } from './crew-usage';

const subjects = (n: number, each = 1) => Object.fromEntries(Array.from({ length: n }, (_, i) => [`k.${i}`, each]));

describe('usageFromSubjects', () => {
	it('counts messages and keys, and estimates bytes from the share of the stream', () => {
		const u = usageFromSubjects('kubemoot_crew_memory', { subjects: subjects(4, 2), total: 4 }, 80, 8000);
		expect(u).toMatchObject({ store: 'kubemoot_crew_memory', messages: 8, keys: 4, bytes: 800, bytesEstimated: true, capped: false });
	});

	it('is empty for a crew with no subjects', () => {
		const u = usageFromSubjects('s', { subjects: {}, total: 0 }, 50, 1000);
		expect([u.messages, u.keys, u.bytes, u.capped]).toEqual([0, 0, 0, false]);
	});

	it('does not divide by an empty stream', () => {
		expect(usageFromSubjects('s', { subjects: subjects(2), total: 2 }, 0, 0).bytes).toBe(0);
	});

	it('never says a crew holds more than the stream', () => {
		expect(usageFromSubjects('s', { subjects: subjects(5, 10), total: 5 }, 10, 1000).bytes).toBe(1000);
	});

	it('stops counting at the cap, reports the real key count, and says it is a lower bound', () => {
		const page = { subjects: subjects(CREW_ENTRY_CAP + 50), total: CREW_ENTRY_CAP + 50 };
		const u = usageFromSubjects('s', page, 1_000_000, 1_000_000);
		expect(u.messages).toBe(CREW_ENTRY_CAP);
		expect(u.keys).toBe(CREW_ENTRY_CAP + 50);
		expect(u.capped).toBe(true);
	});

	it('is capped when NATS holds more subjects than the page returned', () => {
		const u = usageFromSubjects('s', { subjects: subjects(10), total: 250_000 }, 1_000_000, 1000);
		expect(u.capped).toBe(true);
		expect(u.keys).toBe(250_000);
	});

	it('takes a cap of its own', () => {
		const u = usageFromSubjects('s', { subjects: subjects(10), total: 10 }, 100, 100, 3);
		expect([u.messages, u.capped]).toEqual([3, true]);
	});
});

describe('usageFromObjects', () => {
	it('sums exact sizes', () => {
		const u = usageFromObjects('kubemoot_workspace', [{ name: 'a/b/1', size: 10 }, { name: 'a/b/2', size: 32 }], false);
		expect(u).toMatchObject({ messages: 2, keys: 2, bytes: 42, bytesEstimated: false, capped: false });
	});

	it('carries the cap flag of the listing', () => {
		expect(usageFromObjects('s', [], true).capped).toBe(true);
	});
});

describe('display', () => {
	it('shows a lower bound as more than', () => {
		expect(formatCapped(5000, true)).toBe('more than 5,000');
		expect(formatCapped(1234, false)).toBe('1,234');
	});

	it('builds the subject filter of a KV key pattern', () => {
		expect(kvSubjectFilter('kubemoot_crew_memory', 'team.pilot.>')).toBe('$KV.kubemoot_crew_memory.team.pilot.>');
	});
});
