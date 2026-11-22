import { describe, expect, it } from 'vitest';
import { nextTab, tabFromUrl, tabUrl } from './resource-tab.js';

const url = (s: string) => new URL(s, 'https://page.invalid');

describe('tabFromUrl', () => {
	it('reads yaml and treats anything else as the overview', () => {
		expect(tabFromUrl(url('/crews/a?tab=yaml'))).toBe('yaml');
		expect(tabFromUrl(url('/crews/a'))).toBe('overview');
		expect(tabFromUrl(url('/crews/a?tab=bogus'))).toBe('overview');
	});
});

describe('tabUrl', () => {
	it('sets the tab and keeps the other parameters', () => {
		expect(tabUrl(url('/crews/a?namespace=prod'), 'yaml')).toBe('/crews/a?namespace=prod&tab=yaml');
	});
	it('removes the parameter for the overview', () => {
		expect(tabUrl(url('/crews/a?tab=yaml&namespace=prod'), 'overview')).toBe('/crews/a?namespace=prod');
	});
});

describe('nextTab', () => {
	it('moves with the arrows and wraps', () => {
		expect(nextTab('overview', 'ArrowRight')).toBe('yaml');
		expect(nextTab('yaml', 'ArrowRight')).toBe('overview');
		expect(nextTab('overview', 'ArrowLeft')).toBe('yaml');
	});
	it('jumps with Home and End and ignores other keys', () => {
		expect(nextTab('yaml', 'Home')).toBe('overview');
		expect(nextTab('overview', 'End')).toBe('yaml');
		expect(nextTab('overview', 'a')).toBeNull();
	});
});
