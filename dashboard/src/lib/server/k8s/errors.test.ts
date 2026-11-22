// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { SIGNED_OUT_MESSAGE, failedReading, readingOf } from './errors';

describe('failedReading', () => {
	it('says the account may not read it for a 403, and marks it denied', () => {
		expect(failedReading({ code: 403 }, 'the pod list')).toEqual({ available: false, reason: 'your account may not read the pod list', denied: true });
	});

	it('asks for a new sign-in for a 401, and marks it denied', () => {
		expect(failedReading({ statusCode: 401 }, 'x')).toEqual({ available: false, reason: SIGNED_OUT_MESSAGE, denied: true });
	});

	it('says it is not served for a 404, or says what the caller gave', () => {
		expect(failedReading({ code: 404 }, 'node metrics')).toEqual({ available: false, reason: 'node metrics is not served by this cluster' });
		expect(failedReading({ code: 404 }, 'x', 'x is gone')).toEqual({ available: false, reason: 'x is gone' });
	});

	it('says it could not be read for anything else, and is not denied', () => {
		for (const e of [new Error('boom'), { code: 500 }, null, undefined]) {
			expect(failedReading(e, 'the pod list')).toEqual({ available: false, reason: 'the pod list could not be read' });
		}
	});
});

describe('readingOf', () => {
	it('returns the data of a read that works', async () => {
		expect(await readingOf('x', async () => [1])).toEqual({ available: true, data: [1] });
	});

	it('returns the failure of a read that does not, never an empty reading', async () => {
		expect(await readingOf('x', async () => Promise.reject({ code: 403 }))).toMatchObject({ available: false, denied: true });
	});
});
