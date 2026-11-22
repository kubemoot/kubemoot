import { describe, expect, it } from 'vitest';
import { chartName, chartVersion } from './crew-chart';

describe('chartVersion without a build suffix', () => {
	it('drops the label form of a build suffix from either label', () => {
		expect(chartVersion({ 'app.kubernetes.io/version': '0.7.1_5d22e1fe175b' })).toBe('0.7.1');
		expect(chartVersion({ 'helm.sh/chart': 'kubemoot-fitness-0.7.1_5d22e1fe175b' })).toBe('0.7.1');
	});
});

describe('chartVersion', () => {
	it('takes the version suffix of helm.sh/chart, without a build suffix', () => {
		expect(chartVersion({ 'helm.sh/chart': 'homelab-pilot-crew-0.91.0' })).toBe('0.91.0');
		expect(chartVersion({ 'helm.sh/chart': 'crew-1.2.3+build.4' })).toBe('1.2.3');
	});

	it('falls back to app.kubernetes.io/version when the chart label has no version', () => {
		const labels = { 'helm.sh/chart': 'homelab-pilot-crew', 'app.kubernetes.io/version': '2.0.0' };
		expect(chartVersion(labels)).toBe('2.0.0');
		expect(chartVersion({ 'app.kubernetes.io/version': '2.0.0' })).toBe('2.0.0');
	});

	it('is null without either label', () => {
		expect(chartVersion({})).toBeNull();
		expect(chartVersion(undefined)).toBeNull();
	});
});

describe('chartName', () => {
	it('strips the version suffix', () => {
		expect(chartName({ 'helm.sh/chart': 'homelab-pilot-crew-0.91.0' })).toBe('homelab-pilot-crew');
	});

	it('keeps a chart label without a version and is null without the label', () => {
		expect(chartName({ 'helm.sh/chart': 'homelab-pilot-crew' })).toBe('homelab-pilot-crew');
		expect(chartName({})).toBeNull();
		expect(chartName(undefined)).toBeNull();
	});
});
