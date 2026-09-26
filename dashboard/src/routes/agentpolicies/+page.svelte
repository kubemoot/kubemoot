<script lang="ts">
	import { onMount } from 'svelte';
	import { namespace } from '$stores';
	import { ResourceList } from '$components/layout';
	import { AgentPolicyCard } from '$components/resources';
	import type { AgentPolicy } from '$types/kubemoot.js';
	import { LiveList } from '$lib/client/liveList.svelte';

	const live = new LiveList<AgentPolicy>('agentpolicies');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});
</script>

<ResourceList
	title="Agent Policies"
	count={live.items.length}
	loading={live.loading}
	error={live.error}
	helpText="Policies define behavioral configuration for agents: guardrails, A2A delegation rules, system prompts, and inference parameters. One policy can be shared by multiple agents."
>
	{#snippet children()}
		{#each live.items as policy (policy.metadata.namespace + '/' + policy.metadata.name)}
			<AgentPolicyCard {policy} showNamespace={$namespace === ''} />
		{/each}
	{/snippet}

	{#snippet empty()}
		{#if $namespace === ''}
			<p>No agent policies found in any namespace.</p>
		{:else}
			<p>No agent policies found in namespace <code>{$namespace}</code>.</p>
		{/if}
	{/snippet}
</ResourceList>
