// @vitest-environment node
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// SvelteKit refuses a POST, PUT, PATCH or DELETE that has no Content-Type header
// unless its Origin matches the origin the server derives for itself. Behind
// `kubectl port-forward` the browser's Origin is http:// while the server assumes
// https://, so a browser write without a Content-Type would be refused there.
// Every write the browser code sends therefore names its Content-Type.

const SRC = fileURLToPath(new URL('.', import.meta.url));
const WRITE_METHOD = /method:\s*['"`](POST|PUT|PATCH|DELETE)['"`]/i;
// A method held in a variable cannot be judged here, so it counts as a write.
const COMPUTED_METHOD = /method:\s*[^'"`\s]/;
const CONTENT_TYPE = /['"`]Content-Type['"`]\s*:/i;

function browserSources(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) return entry.name === 'server' ? [] : browserSources(path);
		if (entry.name.endsWith('.test.ts') || entry.name.startsWith('+server.')) return [];
		return /\.(ts|svelte)$/.test(entry.name) ? [path] : [];
	});
}

/** The text of each `fetch(...)` call, matched on balanced parentheses. */
function fetchCalls(text: string): string[] {
	const calls: string[] = [];
	let start = text.indexOf('fetch(');
	while (start !== -1) {
		let depth = 0;
		let end = start + 'fetch'.length;
		for (; end < text.length; end++) {
			if (text[end] === '(') depth++;
			else if (text[end] === ')' && --depth === 0) break;
		}
		calls.push(text.slice(start, end + 1));
		start = text.indexOf('fetch(', end);
	}
	return calls;
}

function writesWithoutContentType(text: string): string[] {
	return fetchCalls(text).filter(
		(call) => (WRITE_METHOD.test(call) || COMPUTED_METHOD.test(call)) && !CONTENT_TYPE.test(call)
	);
}

describe('fetchCalls', () => {
	it('returns each call with nested parentheses intact', () => {
		const text = "fetch(`${resolve('/a')}?x`, { method: 'DELETE' }); fetch('/b')";
		expect(fetchCalls(text)).toEqual(["fetch(`${resolve('/a')}?x`, { method: 'DELETE' })", "fetch('/b')"]);
	});

	it('flags a write with no Content-Type and passes one that has it', () => {
		expect(writesWithoutContentType("fetch('/a', { method: 'DELETE' })")).toHaveLength(1);
		expect(
			writesWithoutContentType("fetch('/a', { method: 'DELETE', headers: { 'Content-Type': 'application/json' } })")
		).toEqual([]);
		expect(writesWithoutContentType("fetch('/a')")).toEqual([]);
		expect(writesWithoutContentType("fetch('/a', { method: 'GET' })")).toEqual([]);
	});

	it('flags double-quoted, template and computed write methods without a Content-Type', () => {
		expect(writesWithoutContentType('fetch("/a", { method: "POST" })')).toHaveLength(1);
		expect(writesWithoutContentType('fetch("/a", { method: `patch` })')).toHaveLength(1);
		expect(writesWithoutContentType("fetch('/a', { method: verb })")).toHaveLength(1);
		expect(
			writesWithoutContentType('fetch("/a", { method: "PUT", headers: { "content-type": "application/json" } })')
		).toEqual([]);
	});
});

describe('browser write requests', () => {
	it('all send a Content-Type header', () => {
		const offenders = browserSources(SRC).flatMap((file) =>
			writesWithoutContentType(readFileSync(file, 'utf8')).map((call) => `${relative(SRC, file)}: ${call.slice(0, 80)}`)
		);
		expect(offenders).toEqual([]);
	});

	it('finds the write requests it checks', () => {
		const writes = browserSources(SRC).flatMap((file) =>
			fetchCalls(readFileSync(file, 'utf8')).filter((call) => WRITE_METHOD.test(call))
		);
		expect(writes.length).toBeGreaterThan(10);
	});
});
