import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { SERVICE_ACCOUNT_TOKEN_PATH, serviceAccountToken } from './service-account-token';

describe('serviceAccountToken', () => {
	it('reads the token and trims the trailing newline', () => {
		const file = join(mkdtempSync(join(tmpdir(), 'sa-')), 'token');
		writeFileSync(file, 'abc.def\n');
		expect(serviceAccountToken(file)).toBe('abc.def');
	});

	it('is empty when there is no token file', () => {
		expect(serviceAccountToken('/nonexistent/token')).toBe('');
	});

	it('reads the projected service account path by default', () => {
		expect(SERVICE_ACCOUNT_TOKEN_PATH).toBe('/var/run/secrets/kubernetes.io/serviceaccount/token');
	});
});
