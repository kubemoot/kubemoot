<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { namespace, refreshTrigger } from '#lib/stores/index.js';
	import { DetailPanel } from '#lib/components/layout/index.js';
	import { Section, InfoRow, StatusBadge } from '#lib/components/common/index.js';
	import type { MCPQualityPolicy } from '#lib/types/kubemoot.js';

	let policy = $state<MCPQualityPolicy | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);

	const name = $derived(page.params.name as string);
	const ns = $derived(page.url.searchParams.get('namespace') || $namespace);

	async function fetchPolicy() {
		loading = true;
		error = null;

		try {
			const res = await fetch(`${resolve('/api/kubemoot/mcpqualitypolicies/[name]', { name })}?namespace=${ns}`);
			if (!res.ok) throw new Error('MCP quality policy not found');
			policy = await res.json();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch MCP quality policy';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchPolicy();
	});

	$effect(() => {
		ns;
		$refreshTrigger;
		fetchPolicy();
	});

	const aiEnabled = $derived(policy?.spec.considering?.enabled ?? false);
	const allowCount = $derived(policy?.spec.allowing?.length || 0);
	const blockCount = $derived(policy?.spec.blocking?.length || 0);
</script>

<DetailPanel title={name} subtitle="MCPQualityPolicy" {loading} {error}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">MCPQualityPolicy</span>
				<h1 class="title">{name}</h1>
			</div>
			<StatusBadge
				status={aiEnabled ? 'success' : 'warning'}
				label={aiEnabled ? 'AI Enabled' : 'AI Disabled'}
			/>
		</div>
	{/snippet}

	{#snippet children()}
		{#if policy}
			<div class="sections">
				<Section title="Policy Summary">
					<InfoRow label="Allowing Rules" value={allowCount} />
					<InfoRow label="Blocking Rules" value={blockCount} />
					<InfoRow label="AI Evaluation" value={aiEnabled ? 'Enabled' : 'Disabled'} />
					{#if policy.spec.considering}
						<InfoRow label="Fallback Action" value={policy.spec.considering.fallbackAction || 'deny'} />
						<InfoRow label="Agent Ref" value={policy.spec.considering.agentRef} mono />
						<InfoRow label="Confidence Threshold" value={policy.spec.considering.confidenceThreshold || '0.7'} />
						<InfoRow label="Timeout" value={policy.spec.considering.timeoutSeconds ? `${policy.spec.considering.timeoutSeconds}s` : '30s'} />
					{/if}
				</Section>

				{#if policy.spec.allowing && policy.spec.allowing.length > 0}
					<Section title="Allowing Rules ({policy.spec.allowing.length})">
						<div class="rules-list">
							{#each policy.spec.allowing as entry}
								<div class="rule allow">
									{#if entry.name}
										<span class="rule-field">name: <code>{entry.name}</code></span>
									{/if}
									{#if entry.author}
										<span class="rule-field">author: <code>{entry.author}</code></span>
									{/if}
								</div>
							{/each}
						</div>
					</Section>
				{/if}

				{#if policy.spec.blocking && policy.spec.blocking.length > 0}
					<Section title="Blocking Rules ({policy.spec.blocking.length})">
						<div class="rules-list">
							{#each policy.spec.blocking as entry}
								<div class="rule block">
									{#if entry.name}
										<span class="rule-field">
											name ({entry.name.type || 'exact'}): <code>{entry.name.value}</code>
											{#if entry.name.negate}<span class="negate">negated</span>{/if}
										</span>
									{/if}
									{#if entry.author}
										<span class="rule-field">
											author ({entry.author.type || 'exact'}): <code>{entry.author.value}</code>
											{#if entry.author.negate}<span class="negate">negated</span>{/if}
										</span>
									{/if}
									{#if entry.version}
										<span class="rule-field">version: <code>{entry.version}</code></span>
									{/if}
									{#if entry.reason}
										<span class="rule-reason">{entry.reason}</span>
									{/if}
								</div>
							{/each}
						</div>
					</Section>
				{/if}

				<Section title="Status">
					<InfoRow label="Servers Evaluated" value={policy.status?.serversEvaluated ?? 0} />
					<InfoRow label="Servers Allowed" value={policy.status?.serversAllowed ?? 0} />
					<InfoRow label="Servers Blocked" value={policy.status?.serversBlocked ?? 0} />
					<InfoRow label="Last Evaluated" value={policy.status?.lastEvaluated} />
				</Section>

				<Section title="Metadata" collapsible defaultOpen={false}>
					<InfoRow label="Namespace" value={policy.metadata.namespace} />
					<InfoRow label="UID" value={policy.metadata.uid} mono />
					<InfoRow label="Created" value={policy.metadata.creationTimestamp} />
				</Section>
			</div>
		{/if}
	{/snippet}
</DetailPanel>

<style>
	.header-content {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 1rem;
	}

	.title-section {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.kind {
		font-size: 0.75rem;
		font-weight: 500;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.title {
		font-size: 1.75rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.sections {
		display: flex;
		flex-direction: column;
		gap: 1.5rem;
	}

	.rules-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.rule {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		padding: 0.75rem;
		background-color: var(--color-bg-tertiary);
		border-radius: 0.375rem;
		border-left: 3px solid var(--color-text-muted);
	}

	.rule.allow {
		border-left-color: var(--color-success);
	}

	.rule.block {
		border-left-color: var(--color-error);
	}

	.rule-field {
		font-size: 0.8rem;
		color: var(--color-text);
	}

	.rule-field code {
		font-family: var(--font-mono);
		background-color: var(--color-bg-secondary);
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
	}

	.negate {
		font-size: 0.7rem;
		color: var(--color-warning);
		background-color: rgba(234, 179, 8, 0.15);
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
		margin-left: 0.375rem;
	}

	.rule-reason {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		font-style: italic;
	}
</style>
