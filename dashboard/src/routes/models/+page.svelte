<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { namespace } from '#lib/stores/index.js';
	import { ResourceList } from '#lib/components/layout/index.js';
	import { ModelCard, CatalogModelCard } from '#lib/components/resources/index.js';
	import type { Model, ModelServer } from '#lib/types/kubemoot.js';
	import { LiveList } from '#lib/client/liveList.svelte.js';
	import { withoutModelFilter } from '#lib/model-filter.js';
	import { catalogEntries, filterCatalog } from '#lib/model-catalog.js';

	const live = new LiveList<Model>('models');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});

	// Crews ship no Model resources; the models a crew can select are the catalogs the
	// model servers report. Servers belong to the cluster, not to a crew, so they are
	// listed across all namespaces whatever crew is selected.
	const servers = new LiveList<ModelServer>('modelservers');
	onMount(() => {
		servers.start('');
		return () => servers.stop();
	});

	const modelFilter = $derived(page.url.searchParams.get('model') ?? '');
	const serverFilter = $derived(page.url.searchParams.get('server') ?? '');

	const visibleModels = $derived(
		live.items.filter((m) => {
			if (modelFilter && m.spec.model !== modelFilter) return false;
			if (serverFilter && m.spec.serverRef !== serverFilter) return false;
			return true;
		})
	);

	const visibleCatalog = $derived(filterCatalog(catalogEntries(servers.items), modelFilter, serverFilter));
	const total = $derived(visibleModels.length + visibleCatalog.length);

	const filterActive = $derived(modelFilter !== '' || serverFilter !== '');

	function clearFilter() {
		goto(withoutModelFilter(page.url), { replace: true, reset: false });
	}

</script>

<ResourceList title="Models" count={total} loading={live.loading || servers.loading} error={live.error && servers.error} helpText="Models represent LLM chat models managed by the operator. The operator ensures models are pulled and available on the server. GPU load state is shown on the Model Servers page.">
	{#snippet children()}
		{#if filterActive}
			<div class="filter-bar">
				<span class="filter-label">Filter:</span>
				{#if modelFilter}
					<span class="filter-chip">model = <code>{modelFilter}</code></span>
				{/if}
				{#if serverFilter}
					<span class="filter-chip">server = <code>{serverFilter}</code></span>
				{/if}
				<button class="clear-btn" type="button" onclick={clearFilter}>Clear</button>
			</div>
		{/if}
		{#each visibleModels as model (model.metadata.namespace + '/' + model.metadata.name)}
			<ModelCard {model} showNamespace={$namespace === ''} onAction={() => live.refresh()} />
		{/each}
		{#each visibleCatalog as entry (entry.namespace + '/' + entry.server + '/' + entry.model.name)}
			<CatalogModelCard {entry} showNamespace={$namespace === ''} />
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
