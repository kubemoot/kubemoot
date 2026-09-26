<script lang="ts">
	import { onMount } from 'svelte';
	import { base } from '$app/paths';
	import { refreshTrigger, showStandAsides } from '$stores';
	import { DetailPanel } from '$components/layout';
	import { Section, InfoRow, StatusBadge } from '$components/common';
	import type { KubemootConfig } from '$types/kubemoot.js';

	let config = $state<KubemootConfig | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);

	async function fetchConfig() {
		loading = true;
		error = null;

		try {
			const res = await fetch(`${base}/api/kubemoot/config`);
			const data = await res.json();
			// Get the first (and typically only) KubemootConfig
			config = data.items?.[0] || null;
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch Kubemoot config';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchConfig();
	});

	$effect(() => {
		$refreshTrigger;
		fetchConfig();
	});

	const status = $derived(config?.status?.ready ? 'success' : 'pending');
</script>

<DetailPanel title="Kubemoot Config" subtitle="Cluster-scoped configuration" {loading} {error}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">KubemootConfig</span>
				<h1 class="title">{config?.metadata.name || 'Kubemoot Config'}</h1>
			</div>
			{#if config}
				<StatusBadge {status} label={config.status?.ready ? 'Active' : 'Pending'} />
			{/if}
		</div>
	{/snippet}

	{#snippet children()}
		<div class="sections pref-block">
			<Section title="Display Preferences">
				<label class="pref-row">
					<span class="pref-label">
						Show agents that stand aside in consensus
						<span class="pref-hint">When off, agents that stand aside (do no work) are hidden across the Discussions, Fitness, and span-graph views. Stored in this browser.</span>
					</span>
					<input type="checkbox" checked={$showStandAsides} onchange={(e) => showStandAsides.set(e.currentTarget.checked)} />
				</label>
			</Section>
		</div>
		{#if config}
			<div class="sections">
				{#if config.spec.defaultImages}
					<Section title="Default Images">
						<InfoRow label="Agent" value={config.spec.defaultImages.agent} mono />
						<InfoRow label="MCP Gateway" value={config.spec.defaultImages.mcpGateway} mono />
						<InfoRow label="RAG Indexer" value={config.spec.defaultImages.ragIndexer} mono />
					</Section>
				{/if}

				{#if config.spec.defaults}
					<Section title="Defaults">
						<InfoRow label="Model Provider" value={config.spec.defaults.modelProvider} />
						<InfoRow label="Embedding Model" value={config.spec.defaults.embeddingModel} />
						{#if config.spec.defaults.vectorStore}
							<InfoRow label="Vector Store Type" value={config.spec.defaults.vectorStore.type} />
							<InfoRow label="Vector Store Endpoint" value={config.spec.defaults.vectorStore.endpoint} mono />
						{/if}
					</Section>
				{/if}

				<Section title="Status">
					<InfoRow label="Ready" value={config.status?.ready ? 'Yes' : 'No'} />
					<InfoRow label="Message" value={config.status?.message} />
				</Section>

				<Section title="Metadata" collapsible defaultOpen={false}>
					<InfoRow label="Name" value={config.metadata.name} />
					<InfoRow label="UID" value={config.metadata.uid} mono />
					<InfoRow label="Created" value={config.metadata.creationTimestamp} />
				</Section>
			</div>
		{:else if !loading && !error}
			<div class="no-config">
				<p>No KubemootConfig found in the cluster.</p>
				<p class="hint">Create a KubemootConfig resource to configure cluster-wide defaults.</p>
			</div>
		{/if}
	{/snippet}
</DetailPanel>

<style>
	.header-content {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 1rem;
	}

	.title-section {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.kind {
		font-size: 0.75rem;
		font-weight: 500;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.title {
		font-size: 1.75rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.sections {
		display: flex;
		flex-direction: column;
		gap: 1.5rem;
	}

	.pref-block {
		margin-bottom: 1.5rem;
	}

	.pref-row {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 1rem;
		cursor: pointer;
	}

	.pref-label {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		color: var(--color-text);
	}

	.pref-hint {
		font-size: 0.8rem;
		color: var(--color-text-muted);
	}

	.pref-row input[type='checkbox'] {
		margin-top: 0.2rem;
		cursor: pointer;
		flex-shrink: 0;
	}

	.no-config {
		text-align: center;
		padding: 3rem;
		color: var(--color-text-muted);
	}

	.no-config p {
		margin-bottom: 0.5rem;
	}

	.hint {
		font-size: 0.85rem;
		opacity: 0.7;
	}
</style>
