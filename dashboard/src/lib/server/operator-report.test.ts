import { afterEach, describe, expect, it } from 'vitest';
import { resetPrivateEnv, setPrivateEnv } from '../../../vitest-mocks/app-env-private';
import { IN_CLUSTER_REPORT_URL, operatorReportBase } from './operator-report';

describe('operatorReportBase', () => {
	afterEach(() => {
		resetPrivateEnv();
	});

	it('uses the in-cluster report Service by default', () => {
		expect(operatorReportBase()).toBe(IN_CLUSTER_REPORT_URL);
		expect(IN_CLUSTER_REPORT_URL).toMatch(/^http:\/\/kubemoot-operator-report\.kubemoot:8082$/);
	});

	it('uses OPERATOR_REPORT_URL when set, and ignores an empty value', () => {
		setPrivateEnv({ OPERATOR_REPORT_URL: 'https://report.example.test' });
		expect(operatorReportBase()).toBe('https://report.example.test');
		setPrivateEnv({ OPERATOR_REPORT_URL: '' });
		expect(operatorReportBase()).toBe(IN_CLUSTER_REPORT_URL);
	});
});
