import { describe, expect, it } from 'vitest';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import type { PlacementCall } from '#lib/types/bus.js';
import {
	formatSignedError,
	placementCallsOf,
	placementRow,
	placementSummaryLine,
	predictedFirstTokenMs,
	summarizePlacement
} from './placement-panel';

// Calls as the discussion receipt carries them (shape of a real receipt).
const LEARNED: PlacementCall = {
	agent: 'coordinator',
	phase: 'advisory',
	kind: 'simple',
	at: 1792000000500,
	model: 'qwen3:14b',
	server: 'ollama-gpu',
	coldStart: false,
	policy: 'learned',
	reason: 'predicted 2.9s (wait 0.0, load 0.0, prompt 0.6, generate 2.3)',
	alternatives: [
		{
			server: 'ollama-cpu',
			warm: true,
			startsNow: true,
			predicted: { waitMs: 0, loadMs: 0, promptMs: 4100, generateMs: 56900, totalMs: 61000 }
		}
	],
	predicted: { waitMs: 0, loadMs: 0, promptMs: 600, generateMs: 2300, totalMs: 2900 },
	actual: {
		ok: true,
		waitMs: 0,
		firstTokenMs: 700,
		totalMs: 3400,
		inputTokens: 640,
		outputTokens: 190,
		requests: 1
	},
	error: {
		totalAbsMs: 500,
		totalRelative: 0.147,
		totalSignedMs: 500,
		firstTokenAbsMs: 100,
		firstTokenRelative: 0.143
	}
};

const ALGORITHM: PlacementCall = {
	agent: 'kubernetes-specialist',
	phase: 'evaluation',
	kind: 'tool-turn',
	model: 'qwen3:14b',
	server: 'ollama-gpu',
	coldStart: true,
	policy: 'algorithm',
	reason: 'cold-load fits 14000 <= 32605',
	predicted: { waitMs: 0, loadMs: 7000, promptMs: 2100, generateMs: 2300, totalMs: 11400 },
	actual: { ok: true, waitMs: 4200, totalMs: 13800, inputTokens: 4100, outputTokens: 260, requests: 3 },
	error: { totalAbsMs: 2400, totalRelative: 0.174, totalSignedMs: 2400 }
};

const SHADOW: PlacementCall = {
	agent: 'obs-metrics',
	phase: 'triage',
	kind: 'triage',
	model: 'qwen3:8b',
	server: 'ollama-cpu',
	policy: 'shadow',
	reason: 'algorithm: warm, slot 1/1; learned would choose qwen3:8b@ollama-gpu (predicted 1.2s)',
	actual: { ok: true, totalMs: 31000, inputTokens: 360, outputTokens: 148, requests: 1 }
};

function close(calls: unknown, extra: Partial<DiscussionMessage> = {}): DiscussionMessage {
	return {
		messageId: 'c',
		threadId: 't1',
		agentName: 'coordinator',
		messageType: 'thread_close',
		metadata: { receipt: { calls } },
		...extra
	} as unknown as DiscussionMessage;
}

describe('placementCallsOf', () => {
	it('reads the calls of the latest thread_close receipt', () => {
		const older = close([SHADOW]);
		const latest = close([LEARNED, ALGORITHM]);
		const reply = { messageType: 'reply', threadId: 't1' } as DiscussionMessage;
		expect(placementCallsOf([older, reply, latest, reply])).toEqual([LEARNED, ALGORITHM]);
	});

	it('returns nothing without a thread_close, a receipt, or calls', () => {
		expect(placementCallsOf([])).toEqual([]);
		expect(placementCallsOf([{ messageType: 'reply' } as DiscussionMessage])).toEqual([]);
		expect(placementCallsOf([close(undefined)])).toEqual([]);
		expect(
			placementCallsOf([{ messageType: 'thread_close', metadata: {} } as DiscussionMessage])
		).toEqual([]);
	});

	it('drops entries that are not calls', () => {
		expect(placementCallsOf([close([null, 'x', {}, SHADOW])])).toEqual([SHADOW]);
		expect(placementCallsOf([close('not a list')])).toEqual([]);
	});
});

describe('summarizePlacement', () => {
	it('counts the policies and averages the absolute error of the calls with a prediction', () => {
		const s = summarizePlacement([LEARNED, ALGORITHM, SHADOW]);
		expect(s.calls).toBe(3);
		expect(s.byPolicy).toEqual({ learned: 1, algorithm: 1, shadow: 1 });
		expect(s.measured).toBe(2);
		expect(s.meanAbsErrorMs).toBe(1450);
		expect(placementSummaryLine(s)).toBe(
			'3 calls: 1 learned, 1 algorithm, 1 shadow. Mean absolute error 1.4s over 2 calls.'
		);
	});

	it('works the error out when the receipt omits it', () => {
		expect(summarizePlacement([{ ...LEARNED, error: undefined }]).meanAbsErrorMs).toBe(500);
	});

	it('says so when no call has a prediction', () => {
		const s = summarizePlacement([SHADOW]);
		expect(s.meanAbsErrorMs).toBeUndefined();
		expect(placementSummaryLine(s)).toBe('1 call: 1 shadow. No predictions to compare.');
	});

	it('is empty for no calls and ignores an unknown policy', () => {
		expect(placementSummaryLine(summarizePlacement([]))).toBe('');
		const odd = { ...SHADOW, policy: 'mystery' } as unknown as PlacementCall;
		const s = summarizePlacement([odd]);
		expect(s.byPolicy).toEqual({ learned: 0, algorithm: 0, shadow: 0 });
		expect(placementSummaryLine(s)).toBe('1 call. No predictions to compare.');
	});
});

describe('formatSignedError', () => {
	it('signs the error and gives the percentage of the measured value', () => {
		expect(formatSignedError(500, 3400)).toBe('+500ms (+14.7%)');
		expect(formatSignedError(-1200, 4800)).toBe('-1.2s (-25.0%)');
	});

	it('leaves the percentage out when the measured value is zero or unknown', () => {
		expect(formatSignedError(500, 0)).toBe('+500ms');
		expect(formatSignedError(500, undefined)).toBe('+500ms');
		expect(formatSignedError(500, Number.NaN)).toBe('+500ms');
	});

	it('is empty when the error is unknown or not a number', () => {
		expect(formatSignedError(undefined, 100)).toBe('');
		expect(formatSignedError(Number.NaN, 100)).toBe('');
		expect(formatSignedError(Number.POSITIVE_INFINITY, 100)).toBe('');
	});
});

describe('predictedFirstTokenMs', () => {
	it('adds wait, load and prompt time', () => {
		expect(predictedFirstTokenMs(ALGORITHM.predicted)).toBe(9100);
		expect(predictedFirstTokenMs(undefined)).toBeUndefined();
		const carried = { waitMs: 1, loadMs: 1, promptMs: 1, generateMs: 1, totalMs: 4, firstTokenMs: 2500 };
		expect(predictedFirstTokenMs(carried)).toBe(2500);
	});
});

describe('placementRow', () => {
	it('shows where the call ran, why, and predicted against measured', () => {
		const row = placementRow(LEARNED);
		expect(row).toMatchObject({
			agent: 'coordinator',
			phase: 'advisory',
			place: 'qwen3:14b@ollama-gpu',
			policy: 'learned',
			coldStart: false,
			hasPrediction: true,
			predictedTotal: '2.9s',
			actualTotal: '3.4s',
			totalError: '+500ms (+14.7%)',
			predictedFirstToken: '600ms',
			actualFirstToken: '700ms',
			firstTokenError: '+100ms (+14.3%)',
			failed: false
		});
		expect(row.alternatives).toEqual([
			{
				place: 'qwen3:14b@ollama-cpu',
				total: '1m01s',
				parts: { wait: '0ms', load: '0ms', prompt: '4.1s', generate: '56.9s' },
				warm: true,
				startsNow: true
			}
		]);
	});

	it('marks a cold start and has no first token when the call did not measure one', () => {
		const row = placementRow(ALGORITHM);
		expect(row.coldStart).toBe(true);
		expect(row.actualFirstToken).toBe('');
		expect(row.firstTokenError).toBe('');
		expect(row.totalError).toBe('+2.4s (+17.4%)');
		expect(row.alternatives).toEqual([]);
	});

	it('shows a call without a prediction as such', () => {
		const row = placementRow(SHADOW);
		expect(row.hasPrediction).toBe(false);
		expect(row.predictedTotal).toBe('');
		expect(row.totalError).toBe('');
		expect(row.actualTotal).toBe('31.0s');
	});

	it('copes with a call that carries almost nothing', () => {
		const row = placementRow({ model: 'm', server: 's', policy: 'learned' });
		expect(row).toMatchObject({
			agent: '',
			phase: '',
			reason: '',
			place: 'm@s',
			hasPrediction: false,
			actualTotal: '',
			failed: false,
			alternatives: []
		});
	});

	it('gives no percentage for a zero measured total and flags a failed call', () => {
		const row = placementRow({
			...ALGORITHM,
			error: undefined,
			actual: { ok: false, totalMs: 0, inputTokens: 0, outputTokens: 0, requests: 1 }
		});
		expect(row.failed).toBe(true);
		expect(row.totalError).toBe('-11.4s');
	});

	it('keeps an alternative without a prediction and names its model when it has one', () => {
		const row = placementRow({
			...LEARNED,
			alternatives: [{ server: 'big', model: 'qwen3:32b', warm: false, startsNow: false }]
		});
		expect(row.alternatives).toEqual([
			{ place: 'qwen3:32b@big', total: 'no prediction', parts: null, warm: false, startsNow: false }
		]);
	});
});
