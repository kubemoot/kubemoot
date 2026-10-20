<script lang="ts">
	import { onMount } from 'svelte';
	import { namespace } from '#lib/stores/index.js';
	import { ResourceList } from '#lib/components/layout/index.js';
	import { ModelServerCard } from '#lib/components/resources/index.js';
	import type { ModelServer } from '#lib/types/kubemoot.js';
	import { LiveList } from '#lib/client/liveList.svelte.js';

	// Push-based live list (watch→SSE); no polling.
	const live = new LiveList<ModelServer>('modelservers');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});
</script>

<ResourceList title="Model Servers" count={live.items.length} loading={live.loading} error={live.error} helpText="Model Servers are the inference backends (e.g., Ollama) that host and serve AI models. The operator validates connectivity.">
	{#snippet children()}
		{#each live.items as server (server.metadata.namespace + '/' + server.metadata.name)}
			<ModelServerCard
				{server}
				showNamespace={$namespace === ''}
				onAction={() => live.refresh()}
			/>
		{/each}
	{/snippet}

	{#snippet empty()}
		{#if $namespace === ''}
			<p>No model servers found in any namespace.</p>
		{:else}
			<p>No model servers found in namespace <code>{$namespace}</code>.</p>
		{/if}
	{/snippet}
</ResourceList>
