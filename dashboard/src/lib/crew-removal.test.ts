import { describe, it, expect, vi } from 'vitest';
import { removalPrompt, removalTooltip, removeCrew } from './crew-removal';

const target = { label: 'Homelab Pilot', namespace: 'kubemoot', name: 'homelab-pilot', agentCount: 4 };

describe('removalPrompt', () => {
	it('names the crew and says its agents and open discussions go with it', () => {
		const text = removalPrompt(target);
		expect(text).toContain('Homelab Pilot');
		expect(text).toContain('4 Agents');
		expect(text).toContain('open discussions');
	});

	it('uses the singular for one agent', () => {
		expect(removalPrompt({ ...target, agentCount: 1 })).toContain('1 Agent ');
	});

	it('does not invent a count when the status has none', () => {
		expect(removalPrompt({ ...target, agentCount: undefined })).toContain('its Agents');
	});
});

describe('removalTooltip', () => {
	it('states the purpose', () => {
		expect(removalTooltip('Pilot')).toContain('Remove crew Pilot');
	});
});

describe('removeCrew', () => {
	it('sends DELETE with the namespace and reports success', async () => {
		const fetcher = vi.fn().mockResolvedValue(new Response('{}', { status: 200 }));
		const result = await removeCrew(fetcher, '/api/kubemoot/crews/pilot', 'a b');
		expect(result).toEqual({ ok: true });
		expect(fetcher).toHaveBeenCalledWith('/api/kubemoot/crews/pilot?namespace=a%20b', { method: 'DELETE' });
	});

	it('returns the server error text', async () => {
		const fetcher = vi.fn().mockResolvedValue(new Response('{"error":"forbidden"}', { status: 403 }));
		expect(await removeCrew(fetcher, '/x', 'ns')).toEqual({ ok: false, error: 'forbidden' });
	});

	it('falls back to the status when the body is not JSON', async () => {
		const fetcher = vi.fn().mockResolvedValue(new Response('boom', { status: 502 }));
		expect(await removeCrew(fetcher, '/x', 'ns')).toEqual({ ok: false, error: 'HTTP 502' });
	});

	it('returns a network failure as an error', async () => {
		const fetcher = vi.fn().mockRejectedValue(new Error('offline'));
		expect(await removeCrew(fetcher, '/x', 'ns')).toEqual({ ok: false, error: 'offline' });
	});
});
