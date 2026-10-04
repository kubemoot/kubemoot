<script lang="ts">
	import { onMount } from 'svelte';
	import { namespace } from '#lib/stores/index.js';
	import { ResourceList } from '#lib/components/layout/index.js';
	import { MCPQualityPolicyCard } from '#lib/components/resources/index.js';
	import type { MCPQualityPolicy } from '#lib/types/kubemoot.js';
	import { LiveList } from '#lib/client/liveList.svelte.js';

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
