import { afterEach, describe, expect, it, vi } from 'vitest';
import { loadCollapsed, saveCollapsed } from './list-collapse';

afterEach(() => {
	vi.unstubAllGlobals();
	localStorage.clear();
});

describe('the remembered list collapse', () => {
	it('shows the list until the viewer collapses it, per page', () => {
		expect(loadCollapsed('/moot')).toBe(false);
		saveCollapsed('/moot', true);
		expect(loadCollapsed('/moot')).toBe(true);
		expect(loadCollapsed('/conversations')).toBe(false);
		saveCollapsed('/moot', false);
		expect(loadCollapsed('/moot')).toBe(false);
	});

	it('reads anything but "true" as shown', () => {
		localStorage.setItem('kubemoot-dashboard-list-collapsed-moot', 'yes');
		expect(loadCollapsed('/moot')).toBe(false);
	});

	it('does nothing when storage refuses or is missing', () => {
		vi.stubGlobal('localStorage', {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('full');
			}
		});
		expect(loadCollapsed('/moot')).toBe(false);
		expect(() => saveCollapsed('/moot', true)).not.toThrow();
		vi.stubGlobal('localStorage', undefined);
		expect(loadCollapsed('/moot')).toBe(false);
		expect(() => saveCollapsed('/moot', true)).not.toThrow();
	});
});
