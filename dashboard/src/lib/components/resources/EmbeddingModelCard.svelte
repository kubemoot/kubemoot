<script lang="ts">
	import { base } from '$app/paths';
	import type { EmbeddingModel } from '$types/kubemoot.js';
	import ResourceCard from './ResourceCard.svelte';

	interface Props {
		model: EmbeddingModel;
		showNamespace?: boolean;
	}

	let { model, showNamespace = false }: Props = $props();

	const stateToStatus = {
		Available: 'success',
		Loaded: 'success',
		Pulling: 'pending',
		Pending: 'pending',
		Error: 'error'
	} as const;

	const status = $derived(
		stateToStatus[model.status?.state as keyof typeof stateToStatus] || 'unknown'
	);

	const statusLabel = $derived(model.status?.state || 'Unknown');
</script>

<ResourceCard
	name={model.metadata.name}
	kind="Embedding Model"
	href="{base}/embeddingmodels/{model.metadata.name}?namespace={model.metadata.namespace}"
	{status}
	{statusLabel}
	namespace={showNamespace ? model.metadata.namespace : undefined}
>
	{#snippet children()}
		<div class="info">
			<div class="row">
				<span class="label">Model ID</span>
				<span class="value mono">{model.spec.model}</span>
			</div>
			<div class="row">
				<span class="label">Provider</span>
				<span class="value">{model.spec.providerRef}</span>
			</div>
			{#if model.status?.modelInfo?.dimensions || model.spec.dimensions}
				<div class="row">
					<span class="label">Vector Dims</span>
					<span class="value">{model.status?.modelInfo?.dimensions || model.spec.dimensions}</span>
				</div>
			{/if}
		</div>
	{/snippet}
</ResourceCard>

<style>
	.info {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 0.8rem;
	}

	.label {
		color: var(--color-text-muted);
	}

	.value {
		color: var(--color-text);
	}

	.value.mono {
		font-family: var(--font-mono);
		font-size: 0.75rem;
	}
</style>
