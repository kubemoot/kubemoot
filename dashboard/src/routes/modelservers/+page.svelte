<script lang="ts">
	import { onMount } from 'svelte';
	import { ResourceList } from '#lib/components/layout/index.js';
	import { ModelServerCard } from '#lib/components/resources/index.js';
	import type { ModelServer } from '#lib/types/kubemoot.js';
	import { LiveList } from '#lib/client/liveList.svelte.js';

	// Push-based live list (a watch streamed to the page); no polling. Model servers belong to the cluster,
	// not to a crew, so they are listed across all namespaces whatever crew is selected;
	// scoping them to a crew's namespace would hide every one of them.
	const live = new LiveList<ModelServer>('modelservers');
	onMount(() => {
		live.start('');
		return () => live.stop();
	});
</script>

<ResourceList route="/modelservers" count={live.items.length} loading={live.loading} error={live.error}>
	{#snippet children()}
		{#each live.items as server (server.metadata.namespace + '/' + server.metadata.name)}
			<ModelServerCard {server} showNamespace />
		{/each}
	{/snippet}

	{#snippet empty()}
		<p>No model servers found in any namespace.</p>
	{/snippet}
</ResourceList>
