import { describe, expect, it } from 'vitest';
import { kubemootRelease, oldestRunningStart, operatorDeploymentInfo } from './operator-info';

describe('operatorDeploymentInfo', () => {
	it('reads the version label, first container image, and selector', () => {
		expect(
			operatorDeploymentInfo({
				metadata: { labels: { 'app.kubernetes.io/version': '0.345.0' } },
				spec: {
					selector: { matchLabels: { app: 'kubemoot-operator', tier: 'control' } },
					template: { spec: { containers: [{ image: 'ghcr.io/kubemoot/operator:0.345.0' }] } }
				}
			})
		).toEqual({
			operatorVersion: '0.345.0',
			operatorImage: 'ghcr.io/kubemoot/operator:0.345.0',
			podSelector: 'app=kubemoot-operator,tier=control'
		});
	});

	it('reports unknown and an empty selector for a bare Deployment', () => {
		expect(operatorDeploymentInfo({})).toEqual({
			operatorVersion: 'unknown',
			operatorImage: 'unknown',
			podSelector: ''
		});
	});
});

describe('oldestRunningStart', () => {
	it('picks the oldest Running pod and ignores other phases', () => {
		expect(
			oldestRunningStart([
				{ status: { phase: 'Running', startTime: '2026-09-30T10:00:00Z' } },
				{ status: { phase: 'Pending', startTime: '2026-09-29T00:00:00Z' } },
				{ status: { phase: 'Running', startTime: new Date('2026-09-30T08:00:00Z') } }
			])
		).toBe('2026-09-30T08:00:00.000Z');
	});

	it('returns empty when no pod runs or start times are missing or invalid', () => {
		expect(oldestRunningStart([])).toBe('');
		expect(
			oldestRunningStart([
				{ status: { phase: 'Running' } },
				{ status: { phase: 'Running', startTime: 'not a date' } },
				{}
			])
		).toBe('');
	});
});

describe('kubemootRelease', () => {
	it('reads the release from the KubemootConfig status', () => {
		expect(kubemootRelease({ status: { release: '1.2.3-test' } })).toBe('1.2.3-test');
	});

	it('is empty when the status is not written yet', () => {
		expect(kubemootRelease({})).toBe('');
		expect(kubemootRelease({ status: {} })).toBe('');
	});

	it('is empty for an old operator that reports no release, or a missing config', () => {
		expect(kubemootRelease({ status: { release: undefined } })).toBe('');
		expect(kubemootRelease(null)).toBe('');
		expect(kubemootRelease(undefined)).toBe('');
	});

	it('is empty when the field is not a string', () => {
		expect(kubemootRelease({ status: { release: 42 } })).toBe('');
	});
});
