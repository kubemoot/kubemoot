import { describe, expect, it } from 'vitest';
import golden from './kyaml-golden.json';
import { isTypeAmbiguous, needsQuotes, toKyaml } from './kyaml';

// The golden outputs in kyaml-golden.json were written by `kubectl create --dry-run=client
// --validate=false -o kyaml -f -` (kubectl 1.37), which uses the reference encoder, for the inputs
// beside them. The emitter must match them byte for byte.
describe('toKyaml against kubectl output', () => {
	it.each(golden.map((g) => [g.name, g] as const))('%s', (_name, { input, expected }) => {
		expect(toKyaml(input)).toBe(expected);
	});
});

describe('toKyaml documents', () => {
	it('starts with the --- header and ends with a newline', () => {
		expect(toKyaml({})).toBe('---\n{}\n');
		expect(toKyaml([])).toBe('---\n[]\n');
		expect(toKyaml('x')).toBe('---\n"x"\n');
		expect(toKyaml(7)).toBe('---\n7\n');
		expect(toKyaml(null)).toBe('---\nnull\n');
	});

	it('reads undefined, dates and non-finite numbers the way JSON does', () => {
		expect(toKyaml(undefined)).toBe('---\nnull\n');
		expect(toKyaml({ a: undefined, b: 1 })).toBe('---\n{\n  b: 1,\n}\n');
		expect(toKyaml([undefined, NaN])).toBe('---\n[\n  null,\n  null,\n]\n');
		expect(toKyaml({ at: new Date(Date.UTC(2026, 10, 22)) })).toBe('---\n{\n  at: "2026-11-22T00:00:00.000Z",\n}\n');
	});

	it('writes small structures on several lines, never collapsed', () => {
		expect(toKyaml({ a: [1] })).toBe('---\n{\n  a: [\n    1,\n  ],\n}\n');
		expect(toKyaml([{ a: 1 }])).toBe('---\n[{\n  a: 1,\n}]\n');
	});

	it('sorts keys and does not change the input', () => {
		const input = { b: 1, a: 2 };
		expect(toKyaml(input)).toBe('---\n{\n  a: 2,\n  b: 1,\n}\n');
		expect(Object.keys(input)).toEqual(['b', 'a']);
	});

	it('writes a lone surrogate as the replacement character escape', () => {
		expect(toKyaml('a\ud800b')).toBe('---\n"a\\ufffdb"\n');
	});
});

describe('needsQuotes and isTypeAmbiguous', () => {
	it.each(['', 'true', 'No', 'null', '~', '12', '1.5', '1e3', '0x1F', '0b11', '0o7', '011', '1_000', '11:00', '2026-11-22', '.inf', '-.INF', '.nan', 'NaN', 'inf', 'a b', '-a', 'a-', '.a', 'a:b', 'x"y', 'a\nb'])('quotes %j', (s) => {
		expect(needsQuotes(s)).toBe(true);
	});

	it.each(['a', 'name', 'with-dash', 'a.b', 'k8s.io/name', 'under_score', '_', 'caf\u00e9', 'v1alpha1', '1.5.3', '12abc', '9999999999999999999999x'])('leaves %j bare', (s) => {
		expect(needsQuotes(s)).toBe(false);
	});

	it('recognizes a number too large for an integer as a float, and an out-of-range hex as not a number', () => {
		expect(isTypeAmbiguous('99999999999999999999')).toBe(true);
		expect(isTypeAmbiguous('0xFFFFFFFFFFFFFFFFFF')).toBe(false);
	});

	it('refuses an impossible date or time as a timestamp', () => {
		expect(isTypeAmbiguous('2026-02-30')).toBe(false);
		expect(isTypeAmbiguous('2026-13-01')).toBe(false);
		expect(isTypeAmbiguous('2026-11-22T25:00:00Z')).toBe(false);
		expect(isTypeAmbiguous('2026-11-22T10:00:00')).toBe(false);
		expect(isTypeAmbiguous('2026-11-22 10:00:00')).toBe(true);
		expect(isTypeAmbiguous('2026-11-22T10:00:00.5+02:00')).toBe(true);
	});
});

describe('toKyaml characters kubectl cannot be fed', () => {
	// kubectl's reader turns U+0085 into a space and refuses control characters, so these have no
	// golden file; the expected text follows the reference encoder's escape table.
	it('escapes the next-line character, delete and the other control characters', () => {
		const text = (s: string) => toKyaml(s).slice('---\n'.length, -1);
		expect(text('a\u0085b')).toBe(String.raw`"a\Nb"`);
		expect(text('a\u007fb')).toBe(String.raw`"a\x7fb"`);
		expect(text('a\u0008b\u000cc\u000bd')).toBe(String.raw`"a\bb\fc\vd"`);
		expect(text('a\u{10ffff}b')).toBe(String.raw`"a\U0010ffffb"`);
	});

	it('keeps a tab in a value and escapes it in a key', () => {
		expect(toKyaml({ 'a\tb': 'c\td' })).toBe('---\n{\n  "a\\tb": "c\td",\n}\n');
	});
});
