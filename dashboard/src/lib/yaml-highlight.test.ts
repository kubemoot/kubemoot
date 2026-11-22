import { describe, expect, it } from 'vitest';
import { highlightLine, highlightYaml } from './yaml-highlight.js';
import { toYaml } from './yaml.js';

const join = (line: string) => highlightLine(line).map((t) => t.text).join('');

describe('highlightLine', () => {
	it('marks a key and its string value', () => {
		expect(highlightLine('kind: Crew')).toEqual([
			{ kind: 'key', text: 'kind' },
			{ kind: 'punct', text: ':' },
			{ kind: 'plain', text: ' ' },
			{ kind: 'string', text: 'Crew' }
		]);
	});
	it('separates the indent and dash from the key', () => {
		const tokens = highlightLine('  - name: x');
		expect(tokens[0]).toEqual({ kind: 'punct', text: '  - ' });
		expect(tokens[1]).toEqual({ kind: 'key', text: 'name' });
	});
	it('classifies numbers, booleans, null and empty containers', () => {
		const kind = (v: string) => highlightLine(`a: ${v}`).at(-1)?.kind;
		expect(kind('12')).toBe('number');
		expect(kind('-1.5e3')).toBe('number');
		expect(kind('true')).toBe('bool');
		expect(kind('null')).toBe('null');
		expect(kind('[]')).toBe('punct');
		expect(kind('"12"')).toBe('string');
	});
	it('keeps a quoted key and a colon inside a quoted value', () => {
		const tokens = highlightLine('"a: b": "x: y"');
		expect(tokens[0]).toEqual({ kind: 'key', text: '"a: b"' });
		expect(tokens.at(-1)).toEqual({ kind: 'string', text: '"x: y"' });
	});
	it('handles a key that opens a block and a bare dashed scalar', () => {
		expect(highlightLine('spec:').map((t) => t.kind)).toEqual(['key', 'punct']);
		expect(highlightLine('- a').map((t) => t.kind)).toEqual(['punct', 'string']);
	});
	it('joins back to the original line, whatever the content', () => {
		for (const line of ['', '  ', 'a:', '- ', '- - x', 'url: http://h:80/p', 'k: "q\\"x"', '    z: 1 2']) {
			expect(join(line)).toBe(line);
		}
	});
});

describe('highlightYaml', () => {
	it('returns one token list per line and drops the final newline', () => {
		expect(highlightYaml('a: 1\nb: 2\n')).toHaveLength(2);
	});
	it('reproduces the writer output exactly', () => {
		const text = toYaml({ kind: 'Crew', metadata: { name: 'x', labels: { 'a/b': 'c' } }, spec: { list: [{ n: 1 }, 'z'], e: [] } });
		expect(highlightYaml(text).map((l) => l.map((t) => t.text).join('')).join('\n') + '\n').toBe(text);
	});
	it('reproduces multi-line strings and quoted keys that contain colons', () => {
		const text = toYaml({ note: 'a\nb: c', 'x: y': { 'k/l': 'v: w' }, list: ['- a', ''] });
		expect(highlightYaml(text).map((l) => l.map((t) => t.text).join('')).join('\n') + '\n').toBe(text);
		expect(highlightLine('"x: y":')[0]).toEqual({ kind: 'key', text: '"x: y"' });
	});
});
