import { describe, expect, it } from 'vitest';
import { calibrationBadge, calibrationRows, hasCalibration, loadTime, memory, tokensPerSecond } from './server-calibration';

describe('formatting', () => {
	it('formats speeds, times, and memory', () => {
		expect(tokensPerSecond(15162)).toBe(`${(15162).toLocaleString()} tok/s`);
		expect(tokensPerSecond(7.4)).toBe('7 tok/s');
		expect(loadTime(2892)).toBe('2.9 s');
		expect(loadTime(450)).toBe('450 ms');
		expect(loadTime(999.6)).toBe('1.0 s');
		expect(memory(9507)).toBe('9.3 GiB');
		expect(memory(800)).toBe('800 MiB');
	});

	it('shows a dash for a missing, zero, negative, or non-numeric value', () => {
		for (const bad of [undefined, null, 0, -5, NaN, Infinity, '12', {}]) {
			expect(tokensPerSecond(bad)).toBe('-');
			expect(loadTime(bad)).toBe('-');
			expect(memory(bad)).toBe('-');
		}
	});
});

describe('calibrationRows', () => {
	it('builds one sorted row per model', () => {
		const rows = calibrationRows({
			phase: 'Calibrated',
			models: [
				{ model: 'qwen3:8b', fits: true, loadMs: 3119, generateTokensPerSecond: 152,
					promptTokensPerSecond: 11748, parallelRequests: 2, parallelGenerateTokensPerSecond: 241,
					residentMiB: 9475 },
				{ model: 'qwen3:32b', fits: false, loadMs: 29527,
					reason: 'spills to the CPU: 22265 of 27771 MiB on the accelerator' }
			]
		});
		expect(rows.map((r) => r.model)).toEqual(['qwen3:32b', 'qwen3:8b']);
		expect(rows[1]).toMatchObject({ fits: true, load: '3.1 s', generate: '152 tok/s',
			prompt: `${(11748).toLocaleString()} tok/s`, parallel: '2 x 241 tok/s', resident: '9.3 GiB', reason: '' });
		expect(rows[0]).toMatchObject({ fits: false, generate: '-', parallel: '-' });
		expect(rows[0].reason).toContain('spills');
	});

	it('has no rows without a calibration or with malformed models', () => {
		expect(calibrationRows(undefined)).toEqual([]);
		expect(calibrationRows(null)).toEqual([]);
		expect(calibrationRows({ phase: 'Calibrating' })).toEqual([]);
		// @ts-expect-error a malformed status from the API
		expect(calibrationRows({ models: 'nope' })).toEqual([]);
		// @ts-expect-error a model entry without a name
		expect(calibrationRows({ models: [{ fits: true }, null] })).toEqual([]);
	});

	it('keeps a reason for a model that fits, and gives one to a model that does not', () => {
		const rows = calibrationRows({ models: [
			{ model: 'a', fits: true, reason: 'parallel batch could not be measured' },
			{ model: 'b', fits: false },
			{ model: 'c', fits: true }
		] });
		expect(rows.map((r) => r.reason)).toEqual(['parallel batch could not be measured', 'Does not fit', '']);
	});

	it('skips a model with an empty name, and shows no parallel speed without both numbers', () => {
		const rows = calibrationRows({ models: [
			{ model: '', fits: true },
			{ model: 'm', fits: true, parallelRequests: 2, parallelGenerateTokensPerSecond: 0 }
		] });
		expect(rows.map((r) => r.model)).toEqual(['m']);
		expect(rows[0].parallel).toBe('-');
	});
});

describe('hasCalibration', () => {
	it('is true for a phase or a measured model, false for nothing or an empty object', () => {
		expect(hasCalibration({ phase: 'Calibrating' })).toBe(true);
		expect(hasCalibration({ models: [{ model: 'm', fits: true }] })).toBe(true);
		expect(hasCalibration({})).toBe(false);
		expect(hasCalibration(undefined)).toBe(false);
		expect(hasCalibration(null)).toBe(false);
	});
});

describe('calibrationBadge', () => {
	it('maps each phase', () => {
		expect(calibrationBadge('Calibrated')).toBe('success');
		expect(calibrationBadge('Calibrating')).toBe('pending');
		expect(calibrationBadge('Waiting')).toBe('pending');
		expect(calibrationBadge('Failed')).toBe('error');
		expect(calibrationBadge(undefined)).toBe('unknown');
		expect(calibrationBadge('Bogus')).toBe('unknown');
	});
});

describe('calibrationRows with skipped and failed models', () => {
	const rows = calibrationRows({
		phase: 'Failed',
		models: [
			{ model: 'qwen3:8b', fits: true, status: 'Measured', loadMs: 3119, generateTokensPerSecond: 152 },
			{ model: 'nomic-embed-text:latest', fits: true, status: 'Skipped',
				reason: 'embedding model: generation speed does not apply' },
			{ model: 'broken:1b', fits: true, status: 'Failed', reason: 'POST /api/generate: status 500' },
			{ model: 'quiet:1b', fits: true, status: 'Failed' }
		]
	});
	const byModel = (name: string) => rows.find((r) => r.model === name)!;

	it('marks the outcome of each model', () => {
		expect(byModel('qwen3:8b').outcome).toBe('measured');
		expect(byModel('nomic-embed-text:latest').outcome).toBe('skipped');
		expect(byModel('broken:1b').outcome).toBe('failed');
	});

	it('keeps the reason of a skipped or failed model', () => {
		expect(byModel('nomic-embed-text:latest').reason).toContain('embedding model');
		expect(byModel('broken:1b').reason).toContain('status 500');
		expect(byModel('quiet:1b').reason).toBe('Could not be measured');
	});

	it('shows no figures for a skipped or failed model, even if the entry carries some', () => {
		const odd = calibrationRows({
			models: [{ model: 'x', fits: true, status: 'Skipped', loadMs: 5000, generateTokensPerSecond: 99,
				residentMiB: 900, parallelRequests: 2, parallelGenerateTokensPerSecond: 50 }]
		})[0];
		expect([odd.load, odd.generate, odd.prompt, odd.parallel, odd.resident]).toEqual(['-', '-', '-', '-', '-']);
	});

	it('treats an entry without a status as measured', () => {
		expect(calibrationRows({ models: [{ model: 'old', fits: true, generateTokensPerSecond: 40 }] })[0])
			.toMatchObject({ outcome: 'measured', generate: '40 tok/s' });
	});
});
