<script lang="ts">
	import { base } from '$app/paths';
	import type { AgentPolicy } from '$types/kubemoot.js';
	import ResourceCard from './ResourceCard.svelte';

	interface Props {
		policy: AgentPolicy;
		showNamespace?: boolean;
	}

	let { policy, showNamespace = false }: Props = $props();

	const status = $derived(
		policy.status?.ready
			? 'success'
			: policy.status?.message
				? 'error'
				: 'pending'
	);

	const statusLabel = $derived(policy.status?.ready ? 'Ready' : policy.status?.message ? 'Error' : 'Pending');
	const role = $derived(policy.spec.a2a?.role || 'specialist');
	const guardrailCount = $derived(
		Object.values(policy.spec.guardrails || {}).filter(v => v !== undefined && v !== false).length
	);
	const referencedByCount = $derived(policy.status?.referencedBy?.length || 0);
</script>

<ResourceCard
	name={policy.metadata.name}
	kind="AgentPolicy"
	href="{base}/agentpolicies/{policy.metadata.name}?namespace={policy.metadata.namespace}"
	{status}
	{statusLabel}
	namespace={showNamespace ? policy.metadata.namespace : undefined}
>
	{#snippet children()}
		<div class="info">
			<div class="row">
				<span class="label">Role</span>
				<span class="value role-badge">{role}</span>
			</div>
			<div class="row">
				<span class="label">Guardrails</span>
				<span class="value">{guardrailCount} configured</span>
			</div>
			{#if policy.spec.inference?.temperature !== undefined}
				<div class="row">
					<span class="label">Temperature</span>
					<span class="value">{policy.spec.inference.temperature}</span>
				</div>
			{/if}
		</div>
	{/snippet}

	{#snippet footer()}
		<div class="badges">
			{#if policy.spec.a2a?.enabled}
				<span class="badge active">A2A</span>
			{/if}
			<span class="badge" class:active={referencedByCount > 0}>
				{referencedByCount} agent{referencedByCount !== 1 ? 's' : ''}
			</span>
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

	.role-badge {
		background-color: var(--color-bg-tertiary);
		padding: 0.125rem 0.5rem;
		border-radius: 0.25rem;
		font-size: 0.75rem;
		text-transform: capitalize;
	}

	.badges {
		display: flex;
		gap: 0.5rem;
	}

	.badge {
		background-color: var(--color-bg-tertiary);
		padding: 0.125rem 0.5rem;
		border-radius: 0.25rem;
		font-size: 0.7rem;
		color: var(--color-text-muted);
	}

	.badge.active {
		background-color: rgba(59, 130, 246, 0.15);
		color: var(--color-primary);
	}
</style>
