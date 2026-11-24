import { afterEach, describe, expect, it, vi } from 'vitest';
import { resetPrivateEnv, setPrivateEnv } from '../../../vitest-mocks/app-env-private';
const mocks = vi.hoisted(() => ({ token: vi.fn() }));
vi.mock('#lib/server/service-account-token.js', () => ({ serviceAccountToken: mocks.token }));

import { IN_CLUSTER_REPORT_URL, operatorAuthHeaders, operatorReportBase } from './operator-report';

describe('operatorReportBase', () => {
	afterEach(() => {
		resetPrivateEnv();
	});

	it('uses the in-cluster report Service by default', () => {
		expect(operatorReportBase()).toBe(IN_CLUSTER_REPORT_URL);
		expect(IN_CLUSTER_REPORT_URL).toMatch(/^https:\/\/kubemoot-operator-report\.kubemoot:8082$/);
	});

	it('uses OPERATOR_REPORT_URL when set, and ignores an empty value', () => {
		setPrivateEnv({ OPERATOR_REPORT_URL: 'https://report.example.test' });
		expect(operatorReportBase()).toBe('https://report.example.test');
		setPrivateEnv({ OPERATOR_REPORT_URL: '' });
		expect(operatorReportBase()).toBe(IN_CLUSTER_REPORT_URL);
	});
});

describe('operatorAuthHeaders', () => {
	it('sends the service account token as the bearer', () => {
		mocks.token.mockReturnValue('sa-token');
		expect(operatorAuthHeaders()).toEqual({ Authorization: 'Bearer sa-token' });
	});

	it('sends no header when there is no token, so the operator answers 401', () => {
		mocks.token.mockReturnValue('');
		expect(operatorAuthHeaders()).toEqual({});
	});
});

describe('operatorReportBase with a plain http override', () => {
	it('says once that the token would cross the network in the clear', () => {
		const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
		setPrivateEnv({ OPERATOR_REPORT_URL: 'HTTP://report.example.test' });
		operatorReportBase();
		operatorReportBase();
		expect(warn).toHaveBeenCalledTimes(1);
		expect(warn.mock.calls[0][0]).toContain('plain http');
		warn.mockRestore();
		resetPrivateEnv();
	});
});
