import { describe, expect, it } from 'vitest';
import { consistencyView, trialsIndependence } from './fitness-consistency';
import type { CrewFitnessSuite } from '#lib/types/kubemoot.js';

type S = Pick<CrewFitnessSuite, 'status'>;

describe('consistencyView', () => {
	it('shows a dash before any trial finishes', () => {
		expect(consistencyView({})).toEqual({ passAllK: '-', toCheck: 0, toCheckNames: [], regressions: undefined });
	});

	it('shows the mean pass^k, the flagged scenarios and regressions', () => {
		const s: S = {
			status: {
				consistency: { meanPassAllK: 42, scenariosToCheck: 1, regressions: 2 },
				scenarios: [
					{ name: 'ok', passAllK: 100 },
					{ name: 'never-passes', checkScenario: true }
				]
			}
		};
		expect(consistencyView(s)).toEqual({
			passAllK: '42%',
			toCheck: 1,
			toCheckNames: ['never-passes'],
			regressions: 2
		});
	});

	it('shows 0% as 0%, not as missing', () => {
		expect(consistencyView({ status: { consistency: { meanPassAllK: 0 } } }).passAllK).toBe('0%');
	});
});

describe('trialsIndependence', () => {
	it('is undefined before the suite starts', () => {
		expect(trialsIndependence({})).toBeUndefined();
		expect(trialsIndependence({ status: { conditions: [] } })).toBeUndefined();
	});

	it('reads the TrialsIndependent condition', () => {
		const cond = (status: string) => ({
			status: { conditions: [{ type: 'TrialsIndependent', status, reason: 'R', message: 'why' }] }
		}) as unknown as S;
		expect(trialsIndependence(cond('True'))).toEqual({ independent: true, reason: 'why' });
		expect(trialsIndependence(cond('False'))).toEqual({ independent: false, reason: 'why' });
	});
});
