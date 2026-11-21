import { describe, expect, it } from 'vitest';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import {
	cleanTimeZone,
	conversationIds,
	exportHref,
	exportLabel,
	exportQuery,
	isConversationId,
	MAX_EXPORT_IDS,
	threadConversationId
} from './conversation-export';

const scope = { namespace: 'crew-demo', crew: 'demo' };
const start = (conversationId?: unknown) =>
	({ messageType: 'thread_start', metadata: { conversationId } }) as unknown as DiscussionMessage;
const thread = (...messages: DiscussionMessage[]) => ({ messages });

describe('isConversationId', () => {
	it('accepts 1 to 128 letters, digits, - and _', () => {
		expect(isConversationId('6733c361')).toBe(true);
		expect(isConversationId('a-b_C9')).toBe(true);
		expect(isConversationId('a'.repeat(128))).toBe(true);
	});
	it.each(['', 'a'.repeat(129), 'a b', 'a/b', '../x', 'a&id=b', 'a\n', 7, null, undefined])('refuses %j', (id) => {
		expect(isConversationId(id)).toBe(false);
	});
});

describe('threadConversationId', () => {
	it('reads the id off the opening message', () => {
		expect(threadConversationId(thread(start('abc')))).toBe('abc');
	});
	it('is absent for an older thread, or an id that is not valid', () => {
		expect(threadConversationId(thread(start()))).toBeUndefined();
		expect(threadConversationId(thread(start('a b')))).toBeUndefined();
		expect(threadConversationId(thread())).toBeUndefined();
	});
	it('ignores an id carried by a message that is not the opening one', () => {
		const other = { messageType: 'agree', metadata: { conversationId: 'zzz' } } as unknown as DiscussionMessage;
		expect(threadConversationId(thread(other))).toBeUndefined();
	});
});

describe('conversationIds', () => {
	it('lists each conversation once, in order, skipping threads without one', () => {
		expect(conversationIds([thread(start('a')), thread(start('b')), thread(start('a')), thread(start())])).toEqual(['a', 'b']);
	});
});

describe('exportQuery', () => {
	it('repeats id for each conversation and names the scope', () => {
		const q = new URLSearchParams(exportQuery(scope, ['a', 'b'], 'America/New_York'));
		expect(q.get('namespace')).toBe('crew-demo');
		expect(q.get('crew')).toBe('demo');
		expect(q.getAll('id')).toEqual(['a', 'b']);
		expect(q.get('timeZone')).toBe('America/New_York');
	});
	it('leaves the zone out when it is missing or not a zone name', () => {
		expect(new URLSearchParams(exportQuery(scope, ['a'])).has('timeZone')).toBe(false);
		expect(new URLSearchParams(exportQuery(scope, ['a'], 'x y&z')).has('timeZone')).toBe(false);
	});
	it('refuses no ids, too many ids and an invalid id', () => {
		expect(() => exportQuery(scope, [])).toThrow();
		expect(() => exportQuery(scope, Array.from({ length: MAX_EXPORT_IDS + 1 }, (_, i) => `c${i}`))).toThrow();
		expect(() => exportQuery(scope, ['ok', 'not ok'])).toThrow();
		expect(() => exportQuery(scope, Array.from({ length: MAX_EXPORT_IDS }, (_, i) => `c${i}`))).not.toThrow();
	});
});

describe('exportHref', () => {
	it('is the endpoint with the query, or null when nothing can be asked for', () => {
		expect(exportHref('/e', scope, ['a'])).toBe('/e?namespace=crew-demo&crew=demo&id=a');
		expect(exportHref('/e', null, ['a'])).toBeNull();
		expect(exportHref('/e', scope, [])).toBeNull();
		expect(exportHref('/e', scope, Array.from({ length: MAX_EXPORT_IDS + 1 }, (_, i) => `c${i}`))).toBeNull();
	});
});

describe('labels and zones', () => {
	it('says how many conversations the download holds', () => {
		expect(exportLabel(1)).toBe('Download conversation');
		expect(exportLabel(3)).toBe('Download 3 conversations');
	});
	it('keeps only zone-shaped names', () => {
		expect(cleanTimeZone('Europe/Paris')).toBe('Europe/Paris');
		expect(cleanTimeZone('Etc/GMT+5')).toBe('Etc/GMT+5');
		expect(cleanTimeZone('a b')).toBeUndefined();
		expect(cleanTimeZone('')).toBeUndefined();
		expect(cleanTimeZone(null)).toBeUndefined();
	});
});
