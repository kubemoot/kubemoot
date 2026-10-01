<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { resolve } from '$app/paths';
	import { namespace, refreshTrigger } from '$stores';
	import { DetailPanel } from '$components/layout';
	import { Section, InfoRow, StatusBadge } from '$components/common';
	import type { EmbeddingModel } from '$types/kubemoot.js';

	let model = $state<EmbeddingModel | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);

	const name = $derived($page.params.name as string);
	const ns = $derived($page.url.searchParams.get('namespace') || $namespace);

	async function fetchModel() {
		loading = true;
		error = null;

		try {
			const res = await fetch(`${resolve('/api/kubemoot/embeddingmodels/[name]', { name })}?namespace=${ns}`);
			if (!res.ok) throw new Error('Embedding model not found');
			model = await res.json();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch embedding model';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchModel();
	});

	$effect(() => {
		ns;
		$refreshTrigger;
		fetchModel();
	});

	const stateToStatus = {
		'Available': 'success',
		'Loaded': 'success',
		'Pulling': 'pending',
		'Pending': 'pending',
		'Error': 'error'
	} as const;

	const status = $derived(
		stateToStatus[model?.status?.state as keyof typeof stateToStatus] || 'unknown'
	);
</script>

<DetailPanel title={name} subtitle="EmbeddingModel" {loading} {error}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">EmbeddingModel</span>
				<h1 class="title">{name}</h1>
			</div>
			{#if model}
				<StatusBadge {status} label={model.status?.state || 'Unknown'} />
			{/if}
		</div>
	{/snippet}

	{#snippet children()}
		{#if model}
			<div class="sections">
				<Section title="Spec">
					<InfoRow label="Model ID" value={model.spec.model} mono />
					<InfoRow label="Provider Ref" value={model.spec.providerRef} />
					<InfoRow label="Vector Dimensions" value={model.spec.dimensions} />
				</Section>

				<Section title="Status">
					<InfoRow label="Ready" value={model.status?.ready ? 'Yes' : 'No'} />
					<InfoRow label="State" value={model.status?.state} />
					<InfoRow label="Endpoint" value={model.status?.endpoint} mono />
					<InfoRow label="Message" value={model.status?.message} />
				</Section>

				{#if model.status?.modelInfo}
					<Section title="Model Info">
						<InfoRow label="Vector Dimensions" value={model.status.modelInfo.dimensions} />
						<InfoRow label="Size" value={model.status.modelInfo.size} />
						<InfoRow label="Digest" value={model.status.modelInfo.digest} mono />
					</Section>
				{/if}

				<Section title="Metadata" collapsible defaultOpen={false}>
					<InfoRow label="Namespace" value={model.metadata.namespace} />
					<InfoRow label="UID" value={model.metadata.uid} mono />
					<InfoRow label="Created" value={model.metadata.creationTimestamp} />
				</Section>
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
</style>
