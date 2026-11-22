import { render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page, setPageUrl } from '../../../../vitest-mocks/app-state.svelte.js';
import Page from './+page.svelte';

const agent = {
	apiVersion: 'kubemoot.ai/v1alpha1',
	kind: 'Agent',
	metadata: { name: 'meta', namespace: 'crew-a', annotations: { 'kubemoot.ai/scale-to-zero': 'true' } },
	spec: { type: 'specialist' },
	status: { phase: 'Running', ready: true, endpoint: 'meta.crew-a.svc:8080' }
};

const json = (body: unknown, ok = true) => Promise.resolve({ ok, json: async () => body });
const NEVER = new Promise<never>(() => undefined);

let answers: Record<string, () => Promise<unknown>>;

beforeEach(() => {
	setPageUrl('/agents/meta?namespace=crew-a');
	page.params = { name: 'meta' };
	answers = {
		'/api/kubemoot/agents/meta?': () => json(agent),
		'/api/kubemoot/agents/meta/replicas': () => json({ desired: 1, ready: 1 }),
		'/api/kubemoot/agents/meta/prompt': () => json({ systemPrompt: 'be brief', configMapName: 'meta-policy' }),
		'/api/nats/kv': () => json({ agents: {} })
	};
	vi.stubGlobal(
		'fetch',
		vi.fn((url: string) => {
			const hit = Object.entries(answers).find(([path]) => String(url).includes(path));
			return hit ? hit[1]() : json({ items: [] });
		})
	);
});

afterEach(() => vi.unstubAllGlobals());

describe('the agent page', () => {
	it('shows the agent while the bus read has not answered, and never waits for it', async () => {
		answers['/api/nats/kv'] = () => NEVER;
		render(Page);
		await waitFor(() => expect(screen.getByText('Ready')).toBeTruthy());
		expect(screen.queryByText('Loading...')).toBeNull();
	});

	it('says how many instances run from the Deployment, not from the Agent status', async () => {
		render(Page);
		await waitFor(() => expect(screen.getByText('1/1')).toBeTruthy());
	});

	it('shows 0/0 for an idle scale-to-zero agent rather than 0 of 1', async () => {
		answers['/api/kubemoot/agents/meta/replicas'] = () => json({ desired: 0, ready: 0 });
		render(Page);
		await waitFor(() => expect(screen.getByText('0/0')).toBeTruthy());
		expect(screen.queryByText('0/1')).toBeNull();
	});

	it('shows no count when the Deployment cannot be read', async () => {
		answers['/api/kubemoot/agents/meta/replicas'] = () => json({ error: 'nope' }, false);
		render(Page);
		await waitFor(() => expect(screen.getByText('Replicas')).toBeTruthy());
		expect(screen.queryByText(/^\d+\/\d+$/)).toBeNull();
	});

	it('asks for the agent once, not once on mount and again from the refresh effect', async () => {
		render(Page);
		await waitFor(() => expect(screen.getByText('Replicas')).toBeTruthy());
		const agentCalls = (fetch as ReturnType<typeof vi.fn>).mock.calls.filter(([u]) => /agents\/meta\?namespace/.test(String(u)));
		expect(agentCalls.length).toBe(1);
	});
});
