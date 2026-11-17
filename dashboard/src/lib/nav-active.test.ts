import { describe, expect, it } from 'vitest';
import { isActivePath } from './nav-active';

const root = '/dashboard/';

describe('isActivePath', () => {
	it('matches the root link only on the root', () => {
		expect(isActivePath(root, '/dashboard/', root)).toBe(true);
		expect(isActivePath(root, '/dashboard', root)).toBe(true);
		expect(isActivePath(root, '/dashboard/models', root)).toBe(false);
	});

	it('matches a link on its own page and the pages beneath it', () => {
		expect(isActivePath('/dashboard/models', '/dashboard/models', root)).toBe(true);
		expect(isActivePath('/dashboard/models', '/dashboard/models/', root)).toBe(true);
		expect(isActivePath('/dashboard/models', '/dashboard/models/qwen3', root)).toBe(true);
	});

	it('does not match a different page that starts with the same text', () => {
		expect(isActivePath('/dashboard/models', '/dashboard/modelservers', root)).toBe(false);
		expect(isActivePath('/dashboard/models', '/dashboard/modelservers/vllm-rig0', root)).toBe(false);
		expect(isActivePath('/dashboard/mcpservers', '/dashboard/mcpserverreports', root)).toBe(false);
	});
});
