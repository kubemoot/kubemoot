<script lang="ts">
	import { errorOnFailure, loadingOnStart } from '#lib/client/refresh-state.js';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { namespace, refreshTrigger } from '#lib/stores/index.js';
	import { DetailPanel } from '#lib/components/layout/index.js';
	import { Section, InfoRow, StatusBadge } from '#lib/components/common/index.js';
	import type { Model } from '#lib/types/kubemoot.js';

	let model = $state<Model | null>(null);
	let loaded = false;
	let loading = $state(true);
	let error = $state<string | null>(null);

	const name = $derived(page.params.name as string);
	const ns = $derived(page.url.searchParams.get('namespace') || $namespace);

	async function fetchModel() {
		loading = loadingOnStart(loaded);
		error = null;

		try {
			const res = await fetch(`${resolve('/api/kubemoot/models/[name]', { name })}?namespace=${ns}`);
			if (!res.ok) throw new Error('Model not found');
			model = await res.json();
		} catch (e) {
			error = errorOnFailure(loaded, e, 'Failed to fetch model');
		} finally {
			loaded = loaded || error === null;
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
		Available: 'success',
		Loaded: 'success',
		Pulling: 'pending',
		Pending: 'pending',
		Error: 'error'
	} as const;

	const status = $derived(
		stateToStatus[model?.status?.state as keyof typeof stateToStatus] || 'unknown'
	);

	const statusLabel = $derived(model?.status?.state || 'Unknown');
</script>

<DetailPanel title={name} subtitle="Model" {loading} {error} resource={model}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">Model</span>
				<h1 class="title">{name}</h1>
			</div>
			{#if model}
				<div class="header-actions">
					<StatusBadge {status} label={statusLabel} />
				</div>
			{/if}
		</div>
	{/snippet}

	{#snippet children()}
		{#if model}
			<div class="sections">
				<Section title="Spec">
					<InfoRow label="Model ID" value={model.spec.model} mono />
					<InfoRow label="Server Ref" value={model.spec.serverRef} />
					<InfoRow label="Quantization" value={model.spec.quantization} />
					<InfoRow label="Context Length" value={model.spec.contextLength} />
				</Section>

				<Section title="Status">
					<InfoRow label="Ready" value={model.status?.ready ? 'Yes' : 'No'} />
					<InfoRow label="State" value={model.status?.state} />
					<InfoRow label="Endpoint" value={model.status?.endpoint} mono />
					<InfoRow label="Message" value={model.status?.message} />
				</Section>

				{#if model.status?.modelInfo}
					<Section title="Model Info">
						<InfoRow label="Size" value={model.status.modelInfo.size} />
						<InfoRow label="Parameters" value={model.status.modelInfo.parameters} />
						<InfoRow label="Family" value={model.status.modelInfo.family} />
						<InfoRow label="Format" value={model.status.modelInfo.format} />
						<InfoRow label="Quantization" value={model.status.modelInfo.quantization} />
						<InfoRow label="Context Length" value={model.status.modelInfo.contextLength} />
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

	.header-actions {
		display: flex;
		align-items: center;
		gap: 0.75rem;
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
