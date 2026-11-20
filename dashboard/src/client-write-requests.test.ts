// @vitest-environment node
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// The dashboard is read-only, so no browser code may send a request that changes state.
// A fetch with a POST, PUT, PATCH or DELETE method, or a method held in a variable that
// cannot be judged here, fails this test; a write belongs in kmctl, CrewForge or kubectl.

const SRC = fileURLToPath(new URL('.', import.meta.url));
const WRITE_METHOD = /method:\s*['"`](POST|PUT|PATCH|DELETE)['"`]/i;
const COMPUTED_METHOD = /method:\s*[^'"`\s]/;

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

function writeCalls(text: string): string[] {
	return fetchCalls(text).filter((call) => WRITE_METHOD.test(call) || COMPUTED_METHOD.test(call));
}

describe('fetchCalls', () => {
	it('returns each call with nested parentheses intact', () => {
		const text = "fetch(`${resolve('/a')}?x`, { method: 'DELETE' }); fetch('/b')";
		expect(fetchCalls(text)).toEqual(["fetch(`${resolve('/a')}?x`, { method: 'DELETE' })", "fetch('/b')"]);
	});
});

describe('writeCalls', () => {
	it('flags every write method, in any quoting, and a method held in a variable', () => {
		expect(writeCalls("fetch('/a', { method: 'DELETE' })")).toHaveLength(1);
		expect(writeCalls('fetch("/a", { method: "POST" })')).toHaveLength(1);
		expect(writeCalls('fetch("/a", { method: `patch` })')).toHaveLength(1);
		expect(writeCalls("fetch('/a', { method: verb })")).toHaveLength(1);
	});

	it('passes a read', () => {
		expect(writeCalls("fetch('/a')")).toEqual([]);
		expect(writeCalls("fetch('/a', { method: 'GET' })")).toEqual([]);
	});
});

describe('browser code', () => {
	it('sends no request that changes state', () => {
		const offenders = browserSources(SRC).flatMap((file) =>
			writeCalls(readFileSync(file, 'utf8')).map((call) => `${relative(SRC, file)}: ${call.slice(0, 80)}`)
		);
		expect(offenders).toEqual([]);
	});

	it('finds the browser sources it checks', () => {
		const calls = browserSources(SRC).flatMap((file) => fetchCalls(readFileSync(file, 'utf8')));
		expect(calls.length).toBeGreaterThan(10);
	});
});
