<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { namespace } from '#lib/stores/index.js';
	import { ResourceList } from '#lib/components/layout/index.js';
	import { ModelCard } from '#lib/components/resources/index.js';
	import type { Model } from '#lib/types/kubemoot.js';
	import { LiveList } from '#lib/client/liveList.svelte.js';
	import { withoutModelFilter } from '#lib/model-filter.js';

	const live = new LiveList<Model>('models');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});

	const modelFilter = $derived(page.url.searchParams.get('model') ?? '');
	const providerFilter = $derived(page.url.searchParams.get('provider') ?? '');

	const visibleModels = $derived(
		live.items.filter((m) => {
			if (modelFilter && m.spec.model !== modelFilter) return false;
			if (providerFilter && m.spec.providerRef !== providerFilter) return false;
			return true;
		})
	);

	const filterActive = $derived(modelFilter !== '' || providerFilter !== '');

	function clearFilter() {
		goto(withoutModelFilter(page.url), { replace: true, reset: false });
	}

</script>

<ResourceList title="Models" count={visibleModels.length} loading={live.loading} error={live.error} helpText="Models represent LLM chat models managed by the operator. The operator ensures models are pulled and available on the provider. GPU load state is shown on the Model Providers page.">
	{#snippet children()}
		{#if filterActive}
			<div class="filter-bar">
				<span class="filter-label">Filter:</span>
				{#if modelFilter}
					<span class="filter-chip">model = <code>{modelFilter}</code></span>
				{/if}
				{#if providerFilter}
					<span class="filter-chip">provider = <code>{providerFilter}</code></span>
				{/if}
				<button class="clear-btn" type="button" onclick={clearFilter}>Clear</button>
			</div>
		{/if}
		{#each visibleModels as model (model.metadata.namespace + '/' + model.metadata.name)}
			<ModelCard {model} showNamespace={$namespace === ''} onAction={() => live.refresh()} />
		{/each}
	{/snippet}

	{#snippet empty()}
		{#if filterActive}
			<p>No models match the current filter.</p>
		{:else if $namespace === ''}
			<p>No models found in any namespace.</p>
		{:else}
			<p>No models found in namespace <code>{$namespace}</code>.</p>
		{/if}
	{/snippet}
</ResourceList>

<style>
	.filter-bar {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		padding: 0.5rem 0.75rem;
		margin-bottom: 0.75rem;
		background-color: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.375rem;
		font-size: 0.8rem;
	}

	.filter-label {
		color: var(--color-text-muted);
		font-weight: 500;
	}

	.filter-chip {
		color: var(--color-text);
	}

	.filter-chip code {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		padding: 0.1rem 0.35rem;
		border-radius: 0.2rem;
		background-color: var(--color-bg-tertiary);
	}

	.clear-btn {
		margin-left: auto;
		font-size: 0.75rem;
		padding: 0.15rem 0.6rem;
		border-radius: 0.25rem;
		border: 1px solid var(--color-border);
		background-color: var(--color-bg-tertiary);
		color: var(--color-text);
		cursor: pointer;
	}

	.clear-btn:hover {
		background-color: var(--color-bg-secondary);
	}
</style>
