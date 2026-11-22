import { describe, expect, it } from 'vitest';
import { identityTitle, shortIdentity } from './identity-label.js';

describe('shortIdentity', () => {
	it('shows a service account by its own name', () => {
		expect(shortIdentity('system:serviceaccount:kubemoot:kubemoot-dashboard-user')).toBe('kubemoot-dashboard-user');
	});

	it('shows a user by the username, whatever its shape', () => {
		expect(shortIdentity('alice')).toBe('alice');
		expect(shortIdentity('oidc:alice@example.org')).toBe('oidc:alice@example.org');
	});

	it('leaves a name that only looks like a service account as it is', () => {
		expect(shortIdentity('system:serviceaccount:kubemoot')).toBe('system:serviceaccount:kubemoot');
		expect(shortIdentity('system:serviceaccount:a:b:c')).toBe('system:serviceaccount:a:b:c');
		expect(shortIdentity('')).toBe('');
	});
});

describe('identityTitle', () => {
	it('carries the full identity', () => {
		expect(identityTitle('system:serviceaccount:kubemoot:x')).toContain('system:serviceaccount:kubemoot:x');
	});
});

describe('the top bar', () => {
	it('truncates the name before anything else moves and keeps Sign out in view', async () => {
		const { readFileSync } = await import('node:fs');
		const source = readFileSync('src/routes/+layout.svelte', 'utf8');
		expect(source).toMatch(/\.who \{[^}]*text-overflow: ellipsis/);
		expect(source).toMatch(/\.who \{[^}]*min-width: 0/);
		expect(source).toMatch(/\.signout \{[^}]*flex-shrink: 0/);
		expect(source).toContain('title={identityTitle(data.user.username)}');
		expect(source).toContain('{shortIdentity(data.user.username)}');
	});
});
