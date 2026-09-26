<script lang="ts">
	import { onMount } from 'svelte';
	import { namespace } from '$stores';
	import { ResourceList } from '$components/layout';
	import { MCPQualityPolicyCard } from '$components/resources';
	import type { MCPQualityPolicy } from '$types/kubemoot.js';
	import { LiveList } from '$lib/client/liveList.svelte';

	const live = new LiveList<MCPQualityPolicy>('mcpqualitypolicies');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});
</script>

<ResourceList title="MCP Quality Policies" count={live.items.length} loading={live.loading} error={live.error} helpText="Quality Policies define allow/block rules for MCP servers discovered from catalogs. Can use AI evaluation for edge cases.">
	{#snippet children()}
		{#each live.items as policy (policy.metadata.namespace + '/' + policy.metadata.name)}
			<MCPQualityPolicyCard {policy} showNamespace={$namespace === ''} />
		{/each}
	{/snippet}

	{#snippet empty()}
		<p>No MCP quality policies found{$namespace ? ` in namespace ${$namespace}` : ''}.</p>
	{/snippet}
</ResourceList>
