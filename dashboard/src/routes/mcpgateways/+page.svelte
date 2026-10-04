<script lang="ts">
	import { onMount } from 'svelte';
	import { namespace } from '#lib/stores/index.js';
	import { ResourceList } from '#lib/components/layout/index.js';
	import { MCPGatewayCard } from '#lib/components/resources/index.js';
	import type { MCPGateway } from '#lib/types/kubemoot.js';
	import { LiveList } from '#lib/client/liveList.svelte.js';

	const live = new LiveList<MCPGateway>('mcpgateways');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});
</script>

<ResourceList title="MCP Gateways" count={live.items.length} loading={live.loading} error={live.error} helpText="MCP Gateways aggregate multiple MCP Servers into a single endpoint. Agents connect to the gateway rather than individual servers.">
	{#snippet children()}
		{#each live.items as gateway (gateway.metadata.namespace + '/' + gateway.metadata.name)}
			<MCPGatewayCard {gateway} showNamespace={$namespace === ''} />
		{/each}
	{/snippet}

	{#snippet empty()}
		<p>No MCP gateways found{$namespace ? ` in namespace ${$namespace}` : ''}.</p>
	{/snippet}
</ResourceList>
