// Behaviour of the namespace scope on real routes, with the cluster and NATS faked.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
	listNamespace: vi.fn(),
	listAgents: vi.fn(),
	getAgent: vi.fn(),
	listCrewNamespaces: vi.fn(),
	getNatsConnection: vi.fn(),
	readDiscussionArtifact: vi.fn(),
	listFitnessObjects: vi.fn(),
	readFitnessTranscript: vi.fn(),
	crdWatchResponse: vi.fn()
}));

vi.mock('#lib/server/k8s/client.js', () => ({ getCoreApi: () => ({ listNamespace: mocks.listNamespace }) }));
vi.mock('#lib/server/k8s/index.js', () => ({
	listAgents: mocks.listAgents,
	getAgent: mocks.getAgent,
	listCrewNamespaces: mocks.listCrewNamespaces,
	getCrewFitnessSuite: vi.fn()
}));
vi.mock('#lib/server/k8s/watch-sse.js', () => ({
	crdWatchResponse: mocks.crdWatchResponse,
	isWatchableCrd: () => true
}));
vi.mock('#lib/server/nats-client.js', () => ({
	getNatsConnection: mocks.getNatsConnection,
	sc: new (class {
		encode = (s: string) => new TextEncoder().encode(s);
		decode = (b: Uint8Array) => new TextDecoder().decode(b);
	})()
}));
vi.mock('#lib/server/nats-object-store.js', () => ({
	readDiscussionArtifact: mocks.readDiscussionArtifact,
	listFitnessObjects: mocks.listFitnessObjects,
	readFitnessTranscript: mocks.readFitnessTranscript
}));

import { resetScopeCache } from '#lib/server/scope.js';
import { GET as agentsGET } from './kubemoot/agents/+server';
import { GET as agentGET } from './kubemoot/agents/[name]/+server';
import { GET as artifactGET } from './kubemoot/discussions/artifact/+server';
import { GET as kvGET } from './nats/kv/+server';
import { GET as watchGET } from './kubemoot/watch/[plural]/+server';
import { POST as publishPOST } from './nats/publish/+server';
import { POST as purgePOST } from './nats/purge/+server';
import { GET as subscribeGET } from './nats/subscribe/+server';
import { GET as historyGET } from './nats/history/+server';
import { GET as pinGET, POST as pinPOST } from './discussions/pin/+server';
import { GET as namespacesGET } from './namespaces/+server';
import { GET as scoresGET } from './kubemoot/crewfitnesssuites/[namespace]/[name]/scores/+server';
import { GET as transcriptGET } from './kubemoot/crewfitnesssuites/[namespace]/[name]/transcript/+server';

type Call = (event: any) => Response | Promise<Response>;
const call = (handler: unknown, event: Record<string, unknown>) => (handler as Call)(event);
const BASE = 'https://dashboard';
const urlOf = (path: string) => new URL(`${BASE}${path}`);

function agentList(...namespaces: string[]) {
	return {
		apiVersion: 'v1',
		kind: 'AgentList',
		metadata: {},
		items: namespaces.map((namespace, i) => ({ metadata: { name: `agent-${i}`, namespace } }))
	};
}

beforeEach(() => {
	resetScopeCache();
	for (const m of Object.values(mocks)) m.mockReset();
	mocks.listNamespace.mockResolvedValue({ items: [{ metadata: { name: 'team-a' } }, { metadata: { name: 'team-b' } }] });
});

afterEach(() => {
	vi.unstubAllEnvs();
});

describe('scoped dashboard', () => {
	beforeEach(() => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR', 'kubemoot.ai/workshop-team=true');
	});

	it('lists only in-scope agents across all namespaces', async () => {
		mocks.listAgents.mockResolvedValue(agentList('team-a', 'kubemoot', 'team-b', 'default'));
		const body = await (await call(agentsGET, { url: urlOf('/api/kubemoot/agents?namespace=') })).json();
		expect(body.items.map((i: { metadata: { namespace: string } }) => i.metadata.namespace)).toEqual(['team-a', 'team-b']);
	});

	it('lists nothing for a namespace outside the scope, and never asks the cluster', async () => {
		const body = await (await call(agentsGET, { url: urlOf('/api/kubemoot/agents') })).json();
		expect(body.items).toEqual([]);
		expect(mocks.listAgents).not.toHaveBeenCalled();
	});

	it('answers 404 for a get outside the scope and serves one inside it', async () => {
		mocks.getAgent.mockResolvedValue({ metadata: { name: 'x' } });
		const out = await call(agentGET, { params: { name: 'x' }, url: urlOf('/api/kubemoot/agents/x?namespace=kubemoot') });
		expect(out.status).toBe(404);
		expect(mocks.getAgent).not.toHaveBeenCalled();
		const inside = await call(agentGET, { params: { name: 'x' }, url: urlOf('/api/kubemoot/agents/x?namespace=team-a') });
		expect(inside.status).toBe(200);
	});

	it('lists only in-scope crew namespaces for the selector', async () => {
		mocks.listCrewNamespaces.mockResolvedValue([
			{ namespace: 'team-a', crew: 'c', displayName: 'C' },
			{ namespace: 'homelab-pilot', crew: 'p', displayName: 'P' }
		]);
		const body = await (await call(namespacesGET, {})).json();
		expect(body.crews.map((c: { namespace: string }) => c.namespace)).toEqual(['team-a']);
	});

	describe('watch', () => {
		it('refuses a watch on a namespace outside the scope', async () => {
			const res = await call(watchGET, { params: { plural: 'agents' }, url: urlOf('/api/kubemoot/watch/agents?namespace=kubemoot') });
			expect(res.status).toBe(404);
			expect(mocks.crdWatchResponse).not.toHaveBeenCalled();
		});

		it('filters an all-namespaces watch event by event', async () => {
			mocks.crdWatchResponse.mockReturnValue(new Response('stream'));
			await call(watchGET, { params: { plural: 'agents' }, url: urlOf('/api/kubemoot/watch/agents?namespace=') });
			const accept = mocks.crdWatchResponse.mock.calls[0][2] as (o: unknown) => boolean;
			expect(accept({ metadata: { namespace: 'team-a' } })).toBe(true);
			expect(accept({ metadata: { namespace: 'kubemoot' } })).toBe(false);
		});

		it('lets an infrastructure kind watch the operator namespace', async () => {
			mocks.crdWatchResponse.mockReturnValue(new Response('stream'));
			const res = await call(watchGET, { params: { plural: 'modelproviders' }, url: urlOf('/api/kubemoot/watch/modelproviders?namespace=kubemoot') });
			expect(res.status).toBe(200);
		});
	});

	describe('discussion artifacts and fitness data', () => {
		it('refuses an artifact keyed by a namespace outside the scope without reading it', async () => {
			const res = await call(artifactGET, { url: urlOf('/api/kubemoot/discussions/artifact?key=kubemoot/crew/t1/agent/finding-1') });
			expect(res.status).toBe(404);
			expect(mocks.readDiscussionArtifact).not.toHaveBeenCalled();
		});

		it('serves an artifact of an in-scope namespace', async () => {
			mocks.readDiscussionArtifact.mockResolvedValue(new TextEncoder().encode('full text'));
			const res = await call(artifactGET, { url: urlOf('/api/kubemoot/discussions/artifact?key=team-a/crew/t1/agent/finding-1') });
			expect(res.status).toBe(200);
			expect(await res.text()).toBe('full text');
		});

		it('still reports a malformed key as 400 first', async () => {
			const res = await call(artifactGET, { url: urlOf('/api/kubemoot/discussions/artifact?key=../etc') });
			expect(res.status).toBe(400);
		});

		it('refuses fitness scores and transcripts of a namespace outside the scope', async () => {
			const params = { namespace: 'kubemoot', name: 'suite' };
			expect((await call(scoresGET, { params })).status).toBe(404);
			expect((await call(transcriptGET, { params, url: urlOf('/x?key=kubemoot/suite/r.json') })).status).toBe(404);
			expect(mocks.listFitnessObjects).not.toHaveBeenCalled();
			expect(mocks.readFitnessTranscript).not.toHaveBeenCalled();
		});
	});

	it('serves no pins and accepts none, because pins carry no namespace', async () => {
		expect(await (await call(pinGET, {})).json()).toEqual({ pinned: {} });
		expect((await call(pinPOST, { request: new Request(`${BASE}/x`, { method: 'POST', body: '{}' }) })).status).toBe(403);
		expect(mocks.getNatsConnection).not.toHaveBeenCalled();
	});

	describe('NATS', () => {
		it('refuses a key-value bucket outside the allowlist', async () => {
			const res = await call(kvGET, { url: urlOf('/api/nats/kv?bucket=kubemoot_crew_memory') });
			expect(res.status).toBe(403);
			expect(mocks.getNatsConnection).not.toHaveBeenCalled();
		});

		it('returns only the agent-state entries of in-scope namespaces', async () => {
			const entry = (key: string) => ({ key, operation: 'PUT', value: new TextEncoder().encode('{"ok":true}') });
			const kv = {
				watch: async (opts: { initializedFn: () => void }) => ({
					stop: vi.fn(),
					async *[Symbol.asyncIterator]() {
						yield entry('team-a.agent1');
						yield entry('kubemoot.agent2');
						yield entry('team-b.agent3');
						opts.initializedFn();
						yield { key: 'end', operation: 'DEL', value: new Uint8Array() };
					}
				})
			};
			const views = { kv: vi.fn(async () => kv) };
			mocks.getNatsConnection.mockResolvedValue({ jetstream: () => ({ views }) });
			const body = await (await call(kvGET, { url: urlOf('/api/nats/kv') })).json();
			expect(Object.keys(body.agents)).toEqual(['team-a.agent1', 'team-b.agent3']);
		});

		it('refuses to subscribe outside discussion and chat subjects', async () => {
			for (const subject of ['kubemoot.>', '>', 'kubemoot.quality.>', 'kubemoot.discuss.kubemoot.>']) {
				const res = await call(subscribeGET, { url: urlOf(`/api/nats/subscribe?subject=${encodeURIComponent(subject)}`) });
				expect(res.status, subject).toBe(403);
			}
			expect(mocks.getNatsConnection).not.toHaveBeenCalled();
		});

		it('relays only in-scope messages of a wildcard subscription', async () => {
			const messages = [
				{ subject: 'kubemoot.chat.kubemoot.admin', data: new TextEncoder().encode('hidden') },
				{ subject: 'kubemoot.chat.team-a.admin', data: new TextEncoder().encode('shown') }
			];
			let release: () => void = () => {};
			const stopped = new Promise<void>((r) => (release = r));
			const sub = {
				unsubscribe: vi.fn(() => release()),
				async *[Symbol.asyncIterator]() {
					yield* messages;
					await stopped;
				}
			};
			mocks.getNatsConnection.mockResolvedValue({ subscribe: () => sub });
			const res = await call(subscribeGET, { url: urlOf('/api/nats/subscribe?subject=kubemoot.chat.>') });
			const reader = (res.body as ReadableStream<Uint8Array>).getReader();
			const decoder = new TextDecoder();
			const read = async () => JSON.parse(decoder.decode((await reader.read()).value).replace(/^data: /, ''));
			expect(await read()).toMatchObject({ type: 'connected' });
			expect(await read()).toMatchObject({ type: 'message', subject: 'kubemoot.chat.team-a.admin', data: 'shown' });
			await reader.cancel();
			expect(sub.unsubscribe).toHaveBeenCalled();
		});

		it('history refuses other streams and subjects outside the scope', async () => {
			expect((await call(historyGET, { url: urlOf('/api/nats/history?stream=KV_kubemoot_crew_memory') })).status).toBe(403);
			const res = await call(historyGET, { url: urlOf('/api/nats/history?subject=kubemoot.discuss.kubemoot.>') });
			expect(res.status).toBe(403);
			expect(mocks.getNatsConnection).not.toHaveBeenCalled();
		});

		it('publish and purge accept only a literal in-scope namespace', async () => {
			const publish = vi.fn();
			const purge = vi.fn(async () => ({ purged: 3 }));
			mocks.getNatsConnection.mockResolvedValue({
				publish,
				jetstreamManager: async () => ({ streams: { purge } })
			});
			const post = (handler: unknown, body: unknown) =>
				call(handler, { request: new Request(`${BASE}/x`, { method: 'POST', body: JSON.stringify(body) }) });

			expect((await post(publishPOST, { subject: 'kubemoot.chat.kubemoot.admin', data: 'hi' })).status).toBe(403);
			expect((await post(publishPOST, { subject: 'kubemoot.chat.team-a.admin', data: 'hi' })).status).toBe(200);
			expect(publish).toHaveBeenCalledOnce();

			const wildcard = await post(purgePOST, { stream: 'KUBEMOOT_DISCUSS', filter: 'kubemoot.discuss.*.*.*.t1' });
			expect(wildcard.status).toBe(403);
			const other = await post(purgePOST, { stream: 'OTHER', filter: 'kubemoot.discuss.team-a.>' });
			expect(other.status).toBe(403);
			expect(purge).not.toHaveBeenCalled();
			const ok = await post(purgePOST, { stream: 'KUBEMOOT_DISCUSS', filter: 'kubemoot.discuss.team-a.crew.*.t1' });
			expect(ok.status).toBe(200);
			expect(purge).toHaveBeenCalledOnce();
		});
	});
});

describe('unscoped dashboard', () => {
	it('lists every namespace and fetches the namespace it was asked for', async () => {
		mocks.listAgents.mockResolvedValue(agentList('team-a', 'kubemoot'));
		const body = await (await call(agentsGET, { url: urlOf('/api/kubemoot/agents?namespace=') })).json();
		expect(body.items).toHaveLength(2);
		expect(mocks.listAgents).toHaveBeenCalledWith('');
		expect(mocks.listNamespace).not.toHaveBeenCalled();
	});

	it('serves any key-value bucket and any subscription', async () => {
		const res = await call(kvGET, { url: urlOf('/api/nats/kv?bucket=anything') });
		expect(res.status).toBe(200);
		mocks.getNatsConnection.mockResolvedValue({ subscribe: () => ({ unsubscribe: vi.fn(), async *[Symbol.asyncIterator]() {} }) });
		const sub = await call(subscribeGET, { url: urlOf('/api/nats/subscribe') });
		expect(sub.status).toBe(200);
	});
});
