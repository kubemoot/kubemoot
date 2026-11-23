<script lang="ts">
	import { onMount } from 'svelte';
	import { ResourceList } from '#lib/components/layout/index.js';
	import { ModelServerCard } from '#lib/components/resources/index.js';
	import type { ModelServer } from '#lib/types/kubemoot.js';
	import { resolve } from '$app/paths';
	import { refreshTrigger } from '#lib/stores/index.js';
	import { fetchAcceleratorView, type AcceleratorView } from '#lib/client/accelerator-data.js';
	import { serverAccelerator } from '#lib/accelerator-status.js';
	import { measuredUse } from '#lib/accelerator-use.js';
	import { LiveList } from '#lib/client/liveList.svelte.js';

	// Push-based live list (a watch streamed to the page); no polling. Model servers belong to the cluster,
	// not to a crew, so they are listed across all namespaces whatever crew is selected;
	// scoping them to a crew's namespace would hide every one of them.
	const live = new LiveList<ModelServer>('modelservers');
	// Which accelerator each server runs on and how it is working; read apart so its absence only removes those lines.
	let view = $state<AcceleratorView | null>(null);
	$effect(() => {
		$refreshTrigger;
		void fetchAcceleratorView({ nodes: resolve('/api/nodes'), usage: resolve('/api/node-usage'), accelerators: resolve('/api/accelerator-use') }).then((v) => (view = v));
	});

	onMount(() => {
		live.start('');
		return () => live.stop();
	});
</script>

<ResourceList route="/modelservers" count={live.items.length} loading={live.loading} error={live.error}>
	{#snippet children()}
		{#each live.items as server (server.metadata.namespace + '/' + server.metadata.name)}
			<ModelServerCard {server} showNamespace accelerator={view ? serverAccelerator(server, view.nodes, view.holdersByNode, measuredUse(view.use)) : undefined} />
		{/each}
	{/snippet}

	{#snippet empty()}
		<p>No model servers found in any namespace.</p>
	{/snippet}
</ResourceList>
