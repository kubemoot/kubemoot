<script lang="ts">
	import { onMount } from 'svelte';
	import { base } from '$app/paths';
	import { namespace } from '$lib/stores';
	import { refreshTrigger } from '$lib/stores';
	import { TopologyGraph } from '$lib/components/topology';
	import { HelpTooltip } from '$lib/components/common';
	import type { TopologyNode, TopologyEdge } from '$types/kubemoot.js';

	let nodes = $state<TopologyNode[]>([]);
	let edges = $state<TopologyEdge[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);

	function handleResetLayout() {
		sessionStorage.removeItem('kubemoot-topology-positions');
		// Trigger re-fetch to rebuild graph with dagre layout
		fetchTopology();
	}

	async function fetchTopology() {
		loading = true;
		error = null;
		try {
			const res = await fetch(`${base}/api/kubemoot/topology?namespace=${$namespace}`);
			const data = await res.json();
			if (data.error) {
				error = data.error;
				return;
			}
			nodes = data.nodes || [];
			edges = data.edges || [];
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch topology';
		} finally {
			loading = false;
		}
	}

	onMount(() => fetchTopology());

	$effect(() => {
		$namespace;
		$refreshTrigger;
		fetchTopology();
	});
</script>

<div class="topology-page">
	<div class="page-header">
		<div class="title-with-help">
			<h1>Agent Topology</h1>
			<HelpTooltip text="Visualizes agent relationships. Coordinator agents orchestrate work across specialists via async NATS discussions. Specialist agents have focused tools and subscribe to specific discussion channels. Peer agents operate independently." />
		</div>
		<div class="header-actions">
			<button class="reset-layout-btn" onclick={handleResetLayout}>Reset Layout</button>
			<div class="legend">
			<span class="legend-item">
				<span class="legend-dot coordinator"></span> Coordinator
			</span>
			<span class="legend-item">
				<span class="legend-dot specialist"></span> Specialist
			</span>
			<span class="legend-item">
				<span class="legend-dot peer"></span> Peer
			</span>
			<span class="legend-sep"></span>
			<span class="legend-item">
				<span class="legend-ring ready"></span> Ready
			</span>
			<span class="legend-item">
				<span class="legend-ring error"></span> Error
			</span>
			<span class="legend-item">
				<span class="legend-ring pending"></span> Pending
			</span>
		</div>
		</div>
	</div>

	{#if loading && nodes.length === 0}
		<div class="loading">
			<div class="spinner"></div>
			Loading topology...
		</div>
	{:else if error}
		<div class="error-message">{error}</div>
	{:else if nodes.length === 0}
		<div class="empty">No agents found. Deploy agents to see the topology graph.</div>
	{:else}
		<div class="graph-wrapper">
			<TopologyGraph {nodes} {edges} />
		</div>
	{/if}
</div>

<style>
	.topology-page {
		display: flex;
		flex-direction: column;
		height: 100%;
		padding: 1.5rem;
		gap: 1rem;
	}

	.page-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-shrink: 0;
	}

	.title-with-help {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	h1 {
		font-size: 1.25rem;
		font-weight: 600;
		color: var(--color-text);
		margin: 0;
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: 1rem;
	}

	.reset-layout-btn {
		padding: 0.4rem 0.75rem;
		background: var(--color-bg-secondary);
		color: var(--color-text);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		font-size: 0.8rem;
		cursor: pointer;
		transition: background-color 0.15s;
	}

	.reset-layout-btn:hover {
		background: var(--color-bg-tertiary);
	}

	.legend {
		display: flex;
		align-items: center;
		gap: 1rem;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.legend-item {
		display: flex;
		align-items: center;
		gap: 0.375rem;
	}

	.legend-dot {
		width: 10px;
		height: 10px;
		border-radius: 50%;
	}

	.legend-dot.coordinator { background-color: #3b82f6; }
	.legend-dot.specialist { background-color: #10b981; }
	.legend-dot.peer { background-color: #8b5cf6; }

	.legend-ring {
		width: 10px;
		height: 10px;
		border-radius: 50%;
		border: 2px solid;
		background: transparent;
	}

	.legend-ring.ready { border-color: #10b981; }
	.legend-ring.error { border-color: #ef4444; }
	.legend-ring.pending { border-color: #f59e0b; }

	.legend-sep {
		width: 1px;
		height: 16px;
		background-color: var(--color-border);
	}

	.graph-wrapper {
		flex: 1;
		min-height: 0;
	}

	.loading {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 0.75rem;
		height: 300px;
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.spinner {
		width: 20px;
		height: 20px;
		border: 2px solid var(--color-border);
		border-top-color: var(--color-primary);
		border-radius: 50%;
		animation: spin 0.6s linear infinite;
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	.error-message {
		padding: 1rem;
		background-color: rgba(239, 68, 68, 0.1);
		border: 1px solid rgba(239, 68, 68, 0.3);
		border-radius: 0.5rem;
		color: #f87171;
		font-size: 0.875rem;
	}

	.empty {
		display: flex;
		align-items: center;
		justify-content: center;
		height: 300px;
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}
</style>
