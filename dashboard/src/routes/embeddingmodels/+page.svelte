<script lang="ts">
	import { onMount } from 'svelte';
	import { namespace } from '$stores';
	import { ResourceList } from '$components/layout';
	import { EmbeddingModelCard } from '$components/resources';
	import type { EmbeddingModel } from '$types/kubemoot.js';
	import { LiveList } from '$lib/client/liveList.svelte';

	const live = new LiveList<EmbeddingModel>('embeddingmodels');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});
</script>

<ResourceList title="Embedding Models" count={live.items.length} loading={live.loading} error={live.error} helpText="Embedding models convert text into vector representations for semantic search. Used by RAG Sources to index and query documentation. GPU load state is shown on the Model Providers page.">
	{#snippet children()}
		{#each live.items as model (model.metadata.namespace + '/' + model.metadata.name)}
			<EmbeddingModelCard {model} showNamespace={$namespace === ''} />
		{/each}
	{/snippet}

	{#snippet empty()}
		{#if $namespace === ''}
			<p>No embedding models found in any namespace.</p>
		{:else}
			<p>No embedding models found in namespace <code>{$namespace}</code>.</p>
		{/if}
	{/snippet}
</ResourceList>
