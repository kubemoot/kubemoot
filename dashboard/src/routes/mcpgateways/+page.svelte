<script lang="ts">
	import { onMount } from 'svelte';
	import { namespace } from '$stores';
	import { ResourceList } from '$components/layout';
	import { MCPGatewayCard } from '$components/resources';
	import type { MCPGateway } from '$types/kubemoot.js';
	import { LiveList } from '$lib/client/liveList.svelte';

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
