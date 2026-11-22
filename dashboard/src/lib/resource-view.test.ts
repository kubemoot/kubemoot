import { describe, expect, it } from 'vitest';
import { resourceLines, resourceText, viewFromUrl, viewUrl } from './resource-view.js';

const url = (s: string) => new URL(s, 'https://page.invalid');

describe('viewFromUrl', () => {
	it('reads yaml and json, and treats anything else as the description', () => {
		expect(viewFromUrl(url('/crews/a?view=yaml'))).toBe('yaml');
		expect(viewFromUrl(url('/crews/a?view=json'))).toBe('json');
		expect(viewFromUrl(url('/crews/a'))).toBe('description');
		expect(viewFromUrl(url('/crews/a?view=bogus'))).toBe('description');
		expect(viewFromUrl(url('/crews/a?view=description'))).toBe('description');
	});

	it('still opens the YAML for a link made with the older tab parameter', () => {
		expect(viewFromUrl(url('/crews/a?tab=yaml'))).toBe('yaml');
		expect(viewFromUrl(url('/crews/a?tab=other'))).toBe('description');
	});

	it('prefers the view parameter over the older one', () => {
		expect(viewFromUrl(url('/crews/a?tab=yaml&view=json'))).toBe('json');
	});
});

describe('viewUrl', () => {
	it('sets the view and keeps the other parameters and the hash', () => {
		expect(viewUrl(url('/crews/a?namespace=prod#x'), 'json')).toBe('/crews/a?namespace=prod&view=json#x');
	});

	it('removes the parameter for the description', () => {
		expect(viewUrl(url('/crews/a?view=yaml&namespace=prod'), 'description')).toBe('/crews/a?namespace=prod');
	});

	it('drops the older tab parameter when a view is chosen', () => {
		expect(viewUrl(url('/crews/a?tab=yaml'), 'json')).toBe('/crews/a?view=json');
		expect(viewUrl(url('/crews/a?tab=yaml'), 'description')).toBe('/crews/a');
	});
});


const resource = { apiVersion: 'kubemoot.ai/v1alpha1', kind: 'Crew', metadata: { name: 'demo', managedFields: [{ manager: 'kubectl' }] }, spec: { replicas: 2 } };

describe('resourceText', () => {
	it('writes YAML without the managed fields, and with them when asked', () => {
		expect(resourceText(resource, 'yaml', false)).toContain('replicas: 2');
		expect(resourceText(resource, 'yaml', false)).not.toContain('managedFields');
		expect(resourceText(resource, 'yaml', true)).toContain('managedFields:');
	});

	it('writes two-space JSON ending in a newline, parsing back to the shown resource', () => {
		const text = resourceText(resource, 'json', false);
		expect(text.endsWith('}\n')).toBe(true);
		expect(text).toContain('\n  "kind": "Crew",');
		expect(JSON.parse(text).metadata).toEqual({ name: 'demo' });
		expect(JSON.parse(resourceText(resource, 'json', true)).metadata.managedFields).toHaveLength(1);
	});

	it('leaves a Secret\'s contents out of both formats', () => {
		const secret = { kind: 'Secret', metadata: { name: 's' }, data: { token: 'c2VjcmV0' } };
		expect(resourceText(secret, 'yaml', false)).not.toContain('c2VjcmV0');
		expect(resourceText(secret, 'json', true)).not.toContain('c2VjcmV0');
	});
});

describe('resourceLines', () => {
	it('colours each format with its own reader, line for line', () => {
		for (const format of ['yaml', 'json'] as const) {
			const text = resourceText(resource, format, false);
			expect(resourceLines(text, format).map((l) => l.map((t) => t.text).join('')).join('\n') + '\n').toBe(text);
		}
	});
});
