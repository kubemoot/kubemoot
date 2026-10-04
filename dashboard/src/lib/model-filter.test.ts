import { describe, expect, it } from 'vitest';
import { withoutModelFilter } from './model-filter';

describe('withoutModelFilter', () => {
	it('drops the model and provider filters and keeps the base path', () => {
		const url = new URL('https://homelab.example/dashboard/models?model=qwen3%3A8b&provider=gpu-a');
		expect(withoutModelFilter(url)).toBe('/dashboard/models');
	});

	it('keeps unrelated query parameters', () => {
		const url = new URL('https://homelab.example/dashboard/models?namespace=kubemoot&model=x');
		expect(withoutModelFilter(url)).toBe('/dashboard/models?namespace=kubemoot');
	});

	it('returns the path unchanged when no filter is set', () => {
		expect(withoutModelFilter(new URL('https://homelab.example/dashboard/models'))).toBe('/dashboard/models');
	});

	it('leaves the URL it was given untouched', () => {
		const url = new URL('https://homelab.example/dashboard/models?model=x');
		withoutModelFilter(url);
		expect(url.search).toBe('?model=x');
	});

	it('accepts a read-only URL-like value that only offers href', () => {
		const readonlyUrl = Object.freeze({ href: 'https://homelab.example/dashboard/models?provider=p' });
		expect(withoutModelFilter(readonlyUrl)).toBe('/dashboard/models');
	});
});
