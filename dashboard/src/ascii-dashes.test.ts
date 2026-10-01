// @vitest-environment node
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// The ecosystem writes ASCII hyphens: no em dash (U+2014) or en dash (U+2013) in
// UI text, comments, or code.
const UNICODE_DASH = /[\u2013\u2014]/;

function sourceFiles(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) return sourceFiles(path);
		return /\.(ts|js|svelte|css|html)$/.test(entry.name) ? [path] : [];
	});
}

describe('dashboard source text', () => {
	it('uses no em or en dashes', () => {
		const offenders = sourceFiles(fileURLToPath(new URL('.', import.meta.url))).flatMap((file) =>
			readFileSync(file, 'utf8')
				.split('\n')
				.map((line, i) => (UNICODE_DASH.test(line) ? `${file}:${i + 1}` : ''))
				.filter(Boolean)
		);
		expect(offenders).toEqual([]);
	});
});
