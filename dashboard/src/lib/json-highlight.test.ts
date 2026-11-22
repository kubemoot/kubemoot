import { describe, expect, it } from 'vitest';
import { highlightJson, highlightJsonLine } from './json-highlight';

const join = (line: string) => highlightJsonLine(line).map((t) => t.text).join('');
const kinds = (line: string) => highlightJsonLine(line).map((t) => [t.kind, t.text]);

describe('highlightJsonLine', () => {
	it('marks a key and its string value, with the indent kept plain', () => {
		expect(kinds('  "kind": "Crew",')).toEqual([
			['plain', '  '],
			['key', '"kind"'],
			['punct', ':'],
			['plain', ' '],
			['string', '"Crew"'],
			['punct', ',']
		]);
	});

	it('classifies numbers, booleans and null', () => {
		const kind = (v: string) => highlightJsonLine(`"a": ${v}`).at(-1)?.kind;
		expect(highlightJsonLine('"a": 12').at(-1)).toEqual({ kind: 'number', text: '12' });
		expect(highlightJsonLine('"a": -1.5e3').at(-1)?.kind).toBe('number');
		expect(highlightJsonLine('"a": true').at(-1)?.kind).toBe('bool');
		expect(highlightJsonLine('"a": false').at(-1)?.kind).toBe('bool');
		expect(kind('null')).toBe('null');
		expect(kind('"12"')).toBe('string');
	});

	it('marks opening and closing brackets and empty containers as punctuation', () => {
		expect(kinds('"spec": {')).toEqual([['key', '"spec"'], ['punct', ':'], ['plain', ' '], ['punct', '{']]);
		expect(kinds('  },')).toEqual([['plain', '  '], ['punct', '}'], ['punct', ',']]);
		expect(kinds('"items": []').at(-1)).toEqual(['punct', '[]']);
		expect(kinds('"x": {}').at(-1)).toEqual(['punct', '{}']);
	});

	it('marks a bare array item by its value', () => {
		expect(kinds('    "a",')).toEqual([['plain', '    '], ['string', '"a"'], ['punct', ',']]);
		expect(kinds('    3')).toEqual([['plain', '    '], ['number', '3']]);
	});

	it('keeps a quoted key that holds a colon, and a colon and escaped quote inside a value', () => {
		expect(highlightJsonLine('"a: b": "c: \\"d\\""').find((t) => t.kind === 'key')).toEqual({ kind: 'key', text: '"a: b"' });
		expect(highlightJsonLine('"a": "x: y"').at(-1)).toEqual({ kind: 'string', text: '"x: y"' });
	});

	it('joins back to the line exactly, whatever it holds', () => {
		for (const line of ['{', '  "a": "b",', '    "c": [', '    ]', '"k": "v \\" w"', '"u": "\u00e9 \u2713"', '', '  ']) {
			expect(join(line)).toBe(line);
		}
	});
});

describe('highlightJson', () => {
	it('gives one token list per line and drops the final newline', () => {
		const text = JSON.stringify({ a: 1, b: [true, null], c: {} }, null, 2) + '\n';
		const lines = highlightJson(text);
		expect(lines).toHaveLength(text.split('\n').length - 1);
		expect(lines.map((l) => l.map((t) => t.text).join('')).join('\n') + '\n').toBe(text);
	});
});
