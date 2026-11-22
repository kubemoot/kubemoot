<script lang="ts">
	// Kubemoot Dashboard - Overview Page
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { namespace, refreshTrigger, threads, historyLoaded, initDiscussions } from '#lib/stores/index.js';
	import { PageTitle } from '#lib/components/common/index.js';
	import OverviewTile from '#lib/components/overview/OverviewTile.svelte';
	import NeedsAttention from '#lib/components/overview/NeedsAttention.svelte';
	import { overviewNamespace } from '#lib/overview-counts.js';
	import { buildOverview, type OverviewData } from '#lib/overview-model.js';
	import type { NatsStatus } from '#lib/nats/status.js';

	// Each source is read on its own: undefined while it loads, null when it could not be read, so one
	// failing read shows "Unavailable" on its own tile and never a zero, and never blocks the rest.
	let data = $state<OverviewData>({
		componentStatuses: undefined,
		nodes: undefined,
		metrics: undefined,
		pods: undefined,
		crews: undefined,
		modelServers: undefined,
		models: undefined,
		embeddingModels: undefined,
		mcpServers: undefined,
		mcpGateways: undefined,
		ragSources: undefined,
		agents: undefined,
		suites: undefined,
		nats: undefined,
		threads: undefined
	});

	async function read<T>(url: string, pick: (body: never) => T): Promise<T | null> {
		try {
			const r = await fetch(url);
			if (!r.ok) throw new Error(`HTTP ${r.status}`);
			return pick((await r.json()) as never);
		} catch {
			return null;
		}
	}

	const items = (body: { items?: never[] }) => body.items ?? [];

	function listUrl(plural: string, ns: string): string {
		return `${resolve('/api/kubemoot/crews').replace(/crews$/, plural)}?namespace=${ns}`;
	}

	async function readNats(): Promise<NatsStatus | null> {
		return read(resolve('/api/nats/status'), (body: NatsStatus) => body);
	}

	async function refresh(ns: string) {
		const own = (plural: string) => listUrl(plural, overviewNamespace(plural, ns));
		const [componentStatuses, nodes, usage, crews, modelServers, models, embeddingModels, mcpServers, mcpGateways, ragSources, agents, suites, nats] =
			await Promise.all([
				read(resolve('/api/kubemoot/componentstatuses'), (b: never) => b),
				read(resolve('/api/nodes'), (b: { nodes?: never[] }) => b.nodes ?? []),
				read(resolve('/api/node-usage'), (b: { metrics: never; pods: never }) => b),
				read(own('crews'), items),
				read(own('modelservers'), items),
				read(own('models'), items),
				read(own('embeddingmodels'), items),
				read(own('mcpservers'), items),
				read(own('mcpgateways'), items),
				read(own('ragsources'), items),
				read(own('agents'), items),
				read(listUrl('crewfitnesssuites', ns), items),
				readNats()
			]);
		data = {
			threads: undefined,
			componentStatuses,
			nodes,
			metrics: usage?.metrics,
			pods: usage?.pods,
			crews,
			modelServers,
			models,
			embeddingModels,
			mcpServers,
			mcpGateways,
			ragSources,
			agents,
			suites,
			nats
		};
	}

	onMount(() => {
		initDiscussions();
	});

	$effect(() => {
		$refreshTrigger;
		void refresh($namespace);
	});

	const overview = $derived(
		buildOverview({ ...data, threads: $historyLoaded ? [...$threads.values()] : undefined }, Date.now())
	);
</script>

<div class="overview">
	<header class="header">
		<PageTitle route="/" notes={$namespace === '' ? [] : [`Showing the crew namespace ${$namespace}. Nodes, model servers, models and embedding models are shared by every crew and always counted in full.`]} />
	</header>

	<NeedsAttention findings={overview.attention} />

	<div class="grid">
		{#each overview.tiles as tile (tile.key)}
			<OverviewTile {tile} />
		{/each}
	</div>
</div>

<style>
	.overview {
		max-width: 1200px;
	}

	.header {
		margin-bottom: 1.5rem;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
		gap: 1rem;
	}
</style>
