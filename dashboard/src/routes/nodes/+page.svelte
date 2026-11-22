<script lang="ts">
	import { errorOnFailure, loadingOnStart } from '#lib/client/refresh-state.js';
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { refreshTrigger } from '#lib/stores/index.js';
	import { ResourceList } from '#lib/components/layout/index.js';
	import { NodeCard } from '#lib/components/resources/index.js';
	import type { NodeWithAccelerator } from '#lib/types/k8s.js';
	import type { NodeMetric } from '#lib/node-usage.js';
	import { bySeverityThenName, summarizeNode } from '#lib/node-summary.js';
	import type { PodCount, Reading } from '#lib/reading.js';

	let nodes = $state<NodeWithAccelerator[]>([]);
	let metrics = $state<Reading<Record<string, NodeMetric>> | null>(null);
	let pods = $state<Reading<Record<string, PodCount>> | null>(null);
	let loaded = false;
	let loading = $state(true);
	let error = $state<string | null>(null);

	async function fetchNodes() {
		loading = loadingOnStart(loaded);
		error = null;

		try {
			const res = await fetch(resolve('/api/nodes'));
			const data = await res.json();
			if (!res.ok) throw new Error(data.error ?? 'Failed to fetch nodes');
			nodes = data.nodes || [];
		} catch (e) {
			error = errorOnFailure(loaded, e, 'Failed to fetch nodes');
		} finally {
			loaded = loaded || error === null;
			loading = false;
		}
		await fetchUsage();
	}

	// Use is read apart from the nodes: a cluster without metrics, or an account that may not read
	// them, still lists its nodes, and the page says why the bars are missing.
	async function fetchUsage() {
		try {
			const res = await fetch(resolve('/api/node-usage'));
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const data = await res.json();
			metrics = data.metrics;
			pods = data.pods;
		} catch {
			const reason = 'Node use could not be read';
			metrics = { available: false, reason };
			pods = { available: false, reason };
		}
	}

	onMount(() => {
		fetchNodes();
	});

	$effect(() => {
		$refreshTrigger;
		fetchNodes();
	});

	const summaries = $derived(
		nodes
			.map((n) => summarizeNode(n, metrics?.available ? metrics.data[n.metadata.name] : undefined, pods?.available ? pods.data[n.metadata.name] : undefined, pods?.available === true))
			.sort(bySeverityThenName)
	);
	const missing = $derived(metrics && !metrics.available ? metrics.reason : null);
</script>

<ResourceList route="/nodes" count={nodes.length} {loading} {error}>
	{#snippet children()}
		{#if missing}
			<p class="missing" role="status">CPU and memory use are not shown: {missing}</p>
		{/if}
		{#each summaries as summary (summary.name)}
			<NodeCard {summary} />
		{/each}
	{/snippet}

	{#snippet empty()}
		<p>No nodes found in the cluster.</p>
	{/snippet}
</ResourceList>

<style>
	.missing {
		grid-column: 1 / -1;
		margin: 0;
		font-size: 0.8rem;
		color: var(--color-text-muted);
	}
</style>
