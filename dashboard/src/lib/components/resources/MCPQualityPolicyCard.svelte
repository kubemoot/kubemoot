<script lang="ts">
	import { resolve } from '$app/paths';
	import type { MCPQualityPolicy } from '#lib/types/kubemoot.js';
	import ResourceCard from './ResourceCard.svelte';

	interface Props {
		policy: MCPQualityPolicy;
		showNamespace?: boolean;
	}

	let { policy, showNamespace = false }: Props = $props();

	const aiEnabled = $derived(policy.spec.considering?.enabled ?? false);
	const status = $derived(aiEnabled ? 'success' : 'warning');
	const statusLabel = $derived(aiEnabled ? 'AI Enabled' : 'Rules Only');

	const allowCount = $derived(policy.spec.allowing?.length || 0);
	const blockCount = $derived(policy.spec.blocking?.length || 0);
	const evaluated = $derived(policy.status?.serversEvaluated ?? 0);
</script>

<ResourceCard
	name={policy.metadata.name}
	kind="MCPQualityPolicy"
	href="{resolve('/mcpqualitypolicies/[name]', { name: policy.metadata.name })}?namespace={policy.metadata.namespace}"
	{status}
	{statusLabel}
	namespace={showNamespace ? policy.metadata.namespace : undefined}
>
	{#snippet children()}
		<div class="info">
			<div class="row">
				<span class="label">Allowing Rules</span>
				<span class="value">{allowCount}</span>
			</div>
			<div class="row">
				<span class="label">Blocking Rules</span>
				<span class="value">{blockCount}</span>
			</div>
			<div class="row">
				<span class="label">Fallback</span>
				<span class="value">{policy.spec.considering?.fallbackAction || 'deny'}</span>
			</div>
			<div class="row">
				<span class="label">Evaluated</span>
				<span class="value">{evaluated} servers</span>
			</div>
		</div>
	{/snippet}

	{#snippet footer()}
		<div class="stats">
			<span class="stat allowed">{policy.status?.serversAllowed ?? 0} allowed</span>
			<span class="stat blocked">{policy.status?.serversBlocked ?? 0} blocked</span>
		</div>
	{/snippet}
</ResourceCard>

<style>
	.info {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 0.8rem;
	}

	.label {
		color: var(--color-text-muted);
	}

	.value {
		color: var(--color-text);
	}

	.stats {
		display: flex;
		gap: 0.75rem;
	}

	.stat {
		font-size: 0.75rem;
		padding: 0.125rem 0.5rem;
		border-radius: 0.25rem;
	}

	.stat.allowed {
		background-color: rgba(34, 197, 94, 0.15);
		color: var(--color-success);
	}

	.stat.blocked {
		background-color: rgba(239, 68, 68, 0.15);
		color: var(--color-error);
	}
</style>
