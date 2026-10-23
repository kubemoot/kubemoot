import { describe, expect, it } from 'vitest';
import { authDisplay } from './mcp-auth.js';

describe('authDisplay', () => {
	it('names the Secret that holds the key', () => {
		expect(authDisplay({ registry: { authSecretRef: 'example-mcp-auth' } })).toBe('key from Secret example-mcp-auth');
	});
	it('returns null when no Secret is set', () => {
		expect(authDisplay({ registry: {} })).toBeNull();
		expect(authDisplay({})).toBeNull();
		expect(authDisplay(undefined)).toBeNull();
	});
	it('returns null for a blank name', () => {
		expect(authDisplay({ registry: { authSecretRef: '  ' } })).toBeNull();
	});
});
