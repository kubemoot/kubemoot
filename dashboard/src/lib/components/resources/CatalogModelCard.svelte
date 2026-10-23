<script lang="ts">
	import { resolve } from '$app/paths';
	import { contextLabel, parameterLabel, type CatalogEntry } from '#lib/model-catalog.js';
	import ResourceCard from './ResourceCard.svelte';

	interface Props {
		entry: CatalogEntry;
		showNamespace?: boolean;
	}

	let { entry, showNamespace = false }: Props = $props();

	const model = $derived(entry.model);
	const details = $derived(
		[model.size, parameterLabel(model.parameterCount), contextLabel(model.contextLength)].filter(Boolean)
	);
	const href = $derived(
		`${resolve('/modelservers/[name]', { name: entry.server })}?namespace=${encodeURIComponent(entry.namespace)}`
	);
</script>

<ResourceCard
	name={model.name}
	kind="Catalog model"
	{href}
	status={model.present === false ? 'pending' : 'success'}
	statusLabel={model.present === false ? 'Not present' : 'Available'}
	namespace={showNamespace ? entry.namespace : undefined}
>
	{#snippet children()}
		<div class="info">
			<div class="row">
				<span class="label">Server</span>
				<span class="value mono">{entry.server}</span>
			</div>
			{#if details.length > 0}
				<div class="row">
					<span class="label">Size</span>
					<span class="value">{details.join(' / ')}</span>
				</div>
			{/if}
			{#if model.capabilities?.length}
				<div class="caps">
					{#each model.capabilities as cap (cap)}
						<span class="cap">{cap}</span>
					{/each}
				</div>
			{/if}
		</div>
	{/snippet}
</ResourceCard>

<style>
	.info {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		font-size: 0.8rem;
	}
	.row {
		display: flex;
		justify-content: space-between;
		gap: 0.75rem;
	}
	.label {
		color: var(--color-text-muted);
	}
	.value {
		color: var(--color-text);
	}
	.mono {
		font-family: var(--font-mono);
		font-size: 0.75rem;
	}
	.caps {
		display: flex;
		flex-wrap: wrap;
		gap: 0.3rem;
	}
	.cap {
		padding: 0.05rem 0.4rem;
		border-radius: 0.25rem;
		background-color: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		font-size: 0.7rem;
	}
</style>
