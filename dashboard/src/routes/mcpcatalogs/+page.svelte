<script lang="ts">
	import { onMount } from 'svelte';
	import { namespace } from '$stores';
	import { ResourceList } from '$components/layout';
	import { MCPCatalogCard } from '$components/resources';
	import type { MCPCatalog } from '$types/kubemoot.js';
	import { LiveList } from '$lib/client/liveList.svelte';

	const live = new LiveList<MCPCatalog>('mcpcatalogs');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});
</script>

<ResourceList title="MCP Catalogs" count={live.items.length} loading={live.loading} error={live.error} helpText="MCP Catalogs discover MCP servers from external registries and apply quality policies before deploying them.">
	{#snippet children()}
		{#each live.items as catalog (catalog.metadata.namespace + '/' + catalog.metadata.name)}
			<MCPCatalogCard {catalog} showNamespace={$namespace === ''} />
		{/each}
	{/snippet}

	{#snippet empty()}
		<p>No MCP catalogs found{$namespace ? ` in namespace ${$namespace}` : ''}.</p>
	{/snippet}
</ResourceList>
