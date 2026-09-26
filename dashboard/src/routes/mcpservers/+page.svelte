<script lang="ts">
	import { onMount } from 'svelte';
	import { namespace } from '$stores';
	import { ResourceList } from '$components/layout';
	import { MCPServerCard } from '$components/resources';
	import type { MCPServer } from '$types/kubemoot.js';
	import { LiveList } from '$lib/client/liveList.svelte';

	const live = new LiveList<MCPServer>('mcpservers');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});
</script>

<ResourceList title="MCP Servers" count={live.items.length} loading={live.loading} error={live.error} helpText="MCP (Model Context Protocol) Servers expose tools that agents can call — like kubectl, Helm, or NATS commands. Each server runs as a Kubernetes deployment with an MCP Bridge sidecar.">
	{#snippet children()}
		{#each live.items as server (server.metadata.namespace + '/' + server.metadata.name)}
			<MCPServerCard {server} showNamespace={$namespace === ''} />
		{/each}
	{/snippet}

	{#snippet empty()}
		{#if $namespace === ''}
			<p>No MCP servers found in any namespace.</p>
		{:else}
			<p>No MCP servers found in namespace <code>{$namespace}</code>.</p>
		{/if}
	{/snippet}
</ResourceList>
