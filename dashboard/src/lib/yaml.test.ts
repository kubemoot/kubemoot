import { describe, expect, it } from 'vitest';
import { displayResource, toYaml } from './yaml.js';

describe('toYaml', () => {
	it('writes nested objects and scalars', () => {
		expect(toYaml({ a: 1, b: { c: 'x', d: true } })).toBe('a: 1\nb:\n  c: x\n  d: true\n');
	});
	it('writes arrays of scalars and of objects', () => {
		expect(toYaml({ l: ['a', 'b'], o: [{ n: 1, m: 2 }, { n: 3 }] })).toBe(
			'l:\n- a\n- b\no:\n- n: 1\n  m: 2\n- n: 3\n'
		);
	});
	it('quotes strings that would read as another type or break the syntax', () => {
		expect(toYaml({ a: 'true', b: '12', c: 'x: y', d: '', e: '#c' })).toBe(
			'a: "true"\nb: "12"\nc: "x: y"\nd: ""\ne: "#c"\n'
		);
	});
	it('writes empty containers, null and skips undefined', () => {
		expect(toYaml({ a: [], b: {}, c: null, d: undefined })).toBe('a: []\nb: {}\nc: null\n');
	});
	it('writes a multi-line string on one quoted line', () => {
		expect(toYaml({ a: 'x\ny' })).toBe('a: "x\\ny"\n');
	});
	it('handles a scalar root', () => {
		expect(toYaml('x')).toBe('x\n');
	});
});

describe('displayResource', () => {
	it('drops managedFields', () => {
		const out = displayResource({ metadata: { name: 'a', managedFields: [{}] } });
		expect(out.metadata).toEqual({ name: 'a' });
	});
	it('accepts a typed object whose metadata is an interface and leaves the input intact', () => {
		interface Meta { name: string; managedFields?: unknown[] }
		const r: { metadata?: Meta; spec: number } = { metadata: { name: 'a', managedFields: [{}] }, spec: 1 };
		expect(displayResource(r)).toEqual({ metadata: { name: 'a' }, spec: 1 });
		expect(r.metadata?.managedFields).toHaveLength(1);
	});
	it('returns the resource unchanged without managedFields', () => {
		const r = { metadata: { name: 'a' } };
		expect(displayResource(r)).toBe(r);
	});
});
