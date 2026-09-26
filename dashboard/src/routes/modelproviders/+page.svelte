<script lang="ts">
	import { onMount } from 'svelte';
	import { namespace } from '$stores';
	import { ResourceList } from '$components/layout';
	import { ModelProviderCard } from '$components/resources';
	import type { ModelProvider } from '$types/kubemoot.js';
	import { LiveList } from '$lib/client/liveList.svelte';

	// Push-based live list (watch→SSE); no polling.
	const live = new LiveList<ModelProvider>('modelproviders');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});
</script>

<ResourceList title="Model Providers" count={live.items.length} loading={live.loading} error={live.error} helpText="Model Providers are the inference backends (e.g., Ollama) that host and serve AI models. The operator validates connectivity.">
	{#snippet children()}
		{#each live.items as provider (provider.metadata.namespace + '/' + provider.metadata.name)}
			<ModelProviderCard
				{provider}
				showNamespace={$namespace === ''}
				onAction={() => live.refresh()}
			/>
		{/each}
	{/snippet}

	{#snippet empty()}
		{#if $namespace === ''}
			<p>No model providers found in any namespace.</p>
		{:else}
			<p>No model providers found in namespace <code>{$namespace}</code>.</p>
		{/if}
	{/snippet}
</ResourceList>
