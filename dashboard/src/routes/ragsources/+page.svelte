<script lang="ts">
	import { onMount } from 'svelte';
	import { namespace } from '#lib/stores/index.js';
	import { ResourceList } from '#lib/components/layout/index.js';
	import { RAGSourceCard } from '#lib/components/resources/index.js';
	import type { RAGSource } from '#lib/types/kubemoot.js';
	import { LiveList } from '#lib/client/liveList.svelte.js';

	const live = new LiveList<RAGSource>('ragsources');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});
</script>

<ResourceList title="RAG Sources" count={live.items.length} loading={live.loading} error={live.error} helpText="RAG Sources define documentation repositories that are indexed into vector stores for semantic search. Agents query these during conversations to provide grounded answers.">
	{#snippet children()}
		{#each live.items as source (source.metadata.namespace + '/' + source.metadata.name)}
			<RAGSourceCard {source} showNamespace={$namespace === ''} />
		{/each}
	{/snippet}

	{#snippet empty()}
		{#if $namespace === ''}
			<p>No RAG sources found in any namespace.</p>
		{:else}
			<p>No RAG sources found in namespace <code>{$namespace}</code>.</p>
		{/if}
	{/snippet}
</ResourceList>
