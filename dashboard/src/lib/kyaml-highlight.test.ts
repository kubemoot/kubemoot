import { describe, expect, it } from 'vitest';
import golden from './kyaml-golden.json';
import { highlightKyaml, highlightKyamlLine } from './kyaml-highlight';
import { toKyaml } from './kyaml';

const kinds = (line: string) => highlightKyamlLine(line).tokens.map((t) => [t.kind, t.text]);

describe('highlightKyamlLine', () => {
	it('marks a bare key, a string value and the trailing comma', () => {
		expect(kinds('  kind: "Crew",')).toEqual([['plain', '  '], ['key', 'kind'], ['punct', ':'], ['plain', ' '], ['string', '"Crew"'], ['punct', ',']]);
	});

	it('marks a quoted key, and keeps a colon inside a quoted value in the value', () => {
		expect(kinds('"a b": "x: y",')).toEqual([['key', '"a b"'], ['punct', ':'], ['plain', ' '], ['string', '"x: y"'], ['punct', ',']]);
	});

	it('marks a key with dots and slashes', () => {
		expect(kinds('k8s.io/name: 1,')[0]).toEqual(['key', 'k8s.io/name']);
	});

	it('classifies numbers, booleans and null', () => {
		const kind = (v: string) => highlightKyamlLine(`a: ${v},`).tokens.at(-2)?.kind;
		expect(kind('12')).toBe('number');
		expect(kind('1e+21')).toBe('number');
		expect(kind('true')).toBe('bool');
		expect(kind('null')).toBe('null');
		expect(kind('"12"')).toBe('string');
	});

	it('marks brackets, braces and empty containers as punctuation', () => {
		expect(kinds('items: [{')).toEqual([['key', 'items'], ['punct', ':'], ['plain', ' '], ['punct', '[{']]);
		expect(kinds('}, {')).toEqual([['punct', '}, {']]);
		expect(kinds('e: {},').slice(-1)).toEqual([['punct', '{},']]);
	});

	it('marks a list item that is a string', () => {
		expect(kinds('    "x",')).toEqual([['plain', '    '], ['string', '"x"'], ['punct', ',']]);
	});
});

describe('highlightKyaml', () => {
	it('colours a string folded over several lines as one string, to its closing quote', () => {
		const lines = highlightKyaml(toKyaml({ a: 'one\ntwo\n', b: 1 }));
		const flat = lines.map((l) => l.map((t) => `${t.kind}:${t.text}`).join('|'));
		expect(flat).toEqual([
			'punct:---',
			'punct:{',
			'plain:  |key:a|punct::|plain: |string:"\\',
			'plain:     |string:one\\n\\',
			'plain:     |string:two\\n\\',
			'plain:    |string:"|punct:,',
			'plain:  |key:b|punct::|plain: |number:1|punct:,',
			'punct:}'
		]);
	});

	it('does not read a colon or a quote inside a folded string as structure', () => {
		const text = toKyaml({ a: 'k: "v"\nnext, {x}' });
		for (const line of highlightKyaml(text).slice(3, 5)) expect(line.filter((t) => t.kind === 'key')).toEqual([]);
	});

	it('joins back to the original text for every golden output', () => {
		for (const { expected } of golden) {
			const lines = expected.split('\n');
			if (lines.at(-1) === '') lines.pop();
			const joined = highlightKyaml(expected).map((tokens) => tokens.map((t) => t.text).join(''));
			expect(joined).toEqual(lines);
		}
	});
});
