<script lang="ts">
	import { onMount } from 'svelte';
	import { base } from '$app/paths';
	import { refreshTrigger } from '$stores';
	import { ResourceList } from '$components/layout';
	import { NodeCard } from '$components/resources';
	import type { NodeWithGPU } from '$types/k8s.js';

	let nodes = $state<NodeWithGPU[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);

	async function fetchNodes() {
		loading = true;
		error = null;

		try {
			const res = await fetch(`${base}/api/nodes`);
			const data = await res.json();
			nodes = data.nodes || [];
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch nodes';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchNodes();
	});

	$effect(() => {
		$refreshTrigger;
		fetchNodes();
	});

	const gpuNodes = $derived(nodes.filter((n) => n.gpu?.present));
</script>

<ResourceList title="Nodes" count={nodes.length} {loading} {error}>
	{#snippet children()}
		{#each nodes as node (node.metadata.name)}
			<NodeCard {node} />
		{/each}
	{/snippet}

	{#snippet empty()}
		<p>No nodes found in the cluster.</p>
	{/snippet}
</ResourceList>

{#if gpuNodes.length > 0}
	<div class="gpu-summary">
		<h3>GPU Nodes</h3>
		<p>{gpuNodes.length} node{gpuNodes.length !== 1 ? 's' : ''} with GPU capability</p>
	</div>
{/if}

<style>
	.gpu-summary {
		margin-top: 2rem;
		padding: 1rem;
		background-color: rgba(168, 85, 247, 0.1);
		border: 1px solid rgba(168, 85, 247, 0.2);
		border-radius: 0.5rem;
	}

	.gpu-summary h3 {
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-purple);
		margin-bottom: 0.25rem;
	}

	.gpu-summary p {
		font-size: 0.85rem;
		color: var(--color-text-muted);
	}
</style>
