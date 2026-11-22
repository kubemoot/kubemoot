<script lang="ts">
	import { errorOnFailure, loadingOnStart } from '#lib/client/refresh-state.js';
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { refreshTrigger } from '#lib/stores/index.js';
	import { ResourceList } from '#lib/components/layout/index.js';
	import { NodeCard } from '#lib/components/resources/index.js';
	import type { NodeWithAccelerator } from '#lib/types/k8s.js';
	import type { NodeMetric } from '#lib/node-usage.js';
	import { bySeverityThenName, summarizeNode } from '#lib/node-summary.js';
	import type { PodCount, Reading } from '#lib/reading.js';
	import { ROLE_OPTIONS, chosenRole, loadRole, matchesRole, parseRole, saveRole, type NodeRole } from '#lib/node-role.js';

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

	// The role in the address wins; with none there, the one this viewer chose last, else All.
	let remembered = $state<NodeRole | null>(null);
	const role = $derived(chosenRole(page.url.searchParams.get('role'), remembered));

	function openRole(next: NodeRole) {
		const target = new URL(page.url.href);
		target.searchParams.set('role', next);
		void goto(target.pathname + target.search + target.hash, { replace: true, reset: false });
	}

	function pickRole(event: Event) {
		const next = parseRole((event.currentTarget as HTMLSelectElement).value);
		if (!next) return;
		saveRole(next);
		remembered = next;
		openRole(next);
	}

	onMount(() => {
		remembered = loadRole();
		// Put a remembered role into the address, so the page can be shared and reloaded as it is.
		if (parseRole(page.url.searchParams.get('role')) === null && remembered) openRole(remembered);
		fetchNodes();
	});

	$effect(() => {
		$refreshTrigger;
		fetchNodes();
	});

	const shown = $derived(nodes.filter((n) => matchesRole(n, role)));
	const summaries = $derived(
		shown
			.map((n) => summarizeNode(n, metrics?.available ? metrics.data[n.metadata.name] : undefined, pods?.available ? pods.data[n.metadata.name] : undefined, pods?.available === true))
			.sort(bySeverityThenName)
	);
	const missing = $derived(metrics && !metrics.available ? metrics.reason : null);
</script>

<ResourceList route="/nodes" count={shown.length} {loading} {error}>
	{#snippet actions()}
		<select class="role-select" aria-label="Show nodes" value={role} onchange={pickRole}>
			{#each ROLE_OPTIONS as option (option.id)}<option value={option.id}>{option.label}</option>{/each}
		</select>
	{/snippet}

	{#snippet children()}
		{#if missing}
			<p class="missing" role="status">CPU and memory use are not shown: {missing}</p>
		{/if}
		{#each summaries as summary (summary.name)}
			<NodeCard {summary} />
		{/each}
	{/snippet}

	{#snippet empty()}
		<p>{role === 'all' ? 'No nodes found in the cluster.' : `No ${ROLE_OPTIONS.find((o) => o.id === role)?.label.toLowerCase()} in the cluster.`}</p>
	{/snippet}
</ResourceList>

<style>
	.role-select {
		padding: 0.25rem 1.4rem 0.25rem 0.6rem;
		font: inherit;
		font-size: 0.8rem;
		color: var(--color-text);
		background-color: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 0.3rem;
		cursor: pointer;
	}

	.role-select:hover {
		border-color: var(--color-primary);
	}

	.role-select:focus-visible {
		outline: 2px solid var(--color-primary);
		outline-offset: 2px;
	}

	.missing {
		grid-column: 1 / -1;
		margin: 0;
		font-size: 0.8rem;
		color: var(--color-text-muted);
	}
</style>
