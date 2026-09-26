<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { base } from '$app/paths';
	import { namespace, refreshTrigger } from '$stores';
	import { DetailPanel } from '$components/layout';
	import { Section, InfoRow, StatusBadge } from '$components/common';
	import type { AgentPolicy } from '$types/kubemoot.js';

	let policy = $state<AgentPolicy | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);

	const name = $derived($page.params.name);
	const ns = $derived($page.url.searchParams.get('namespace') || $namespace);

	async function fetchPolicy() {
		loading = true;
		error = null;

		try {
			const res = await fetch(`${base}/api/kubemoot/agentpolicies/${name}?namespace=${ns}`);
			if (!res.ok) throw new Error('AgentPolicy not found');
			policy = await res.json();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch agent policy';
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

	const status = $derived(
		policy?.status?.ready
			? 'success'
			: policy?.status?.message
				? 'error'
				: 'pending'
	);
</script>

<DetailPanel title={name} subtitle="AgentPolicy" {loading} {error}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">AgentPolicy</span>
				<h1 class="title">{name}</h1>
			</div>
			{#if policy}
				<StatusBadge {status} label={policy.status?.ready ? 'Ready' : 'Pending'} />
			{/if}
		</div>
	{/snippet}

	{#snippet children()}
		{#if policy}
			<div class="sections">
				{#if policy.spec.guardrails}
					<Section title="Guardrails">
						{#if policy.spec.guardrails.readBeforeWrite !== undefined}
							<InfoRow label="Read Before Write" value={policy.spec.guardrails.readBeforeWrite ? 'Yes' : 'No'} />
						{/if}
						{#if policy.spec.guardrails.confirmDestructive !== undefined}
							<InfoRow label="Confirm Destructive" value={policy.spec.guardrails.confirmDestructive ? 'Yes' : 'No'} />
						{/if}
						{#if policy.spec.guardrails.allowedNamespaces?.length}
							<InfoRow label="Allowed Namespaces" value={policy.spec.guardrails.allowedNamespaces.join(', ')} />
						{/if}
						{#if policy.spec.guardrails.deniedResources?.length}
							<InfoRow label="Denied Resources" value={policy.spec.guardrails.deniedResources.join(', ')} />
						{/if}
						{#if policy.spec.guardrails.maxToolCallsPerTurn !== undefined}
							<InfoRow label="Max Tool Calls/Turn" value={policy.spec.guardrails.maxToolCallsPerTurn} />
						{/if}
						{#if policy.spec.guardrails.maxTokensPerRequest !== undefined}
							<InfoRow label="Max Tokens/Request" value={policy.spec.guardrails.maxTokensPerRequest} />
						{/if}
						{#if policy.spec.guardrails.auditLog !== undefined}
							<InfoRow label="Audit Log" value={policy.spec.guardrails.auditLog ? 'Enabled' : 'Disabled'} />
						{/if}
					</Section>
				{/if}

				{#if policy.spec.a2a}
					<Section title="Discussion Collaboration">
						<p class="section-hint">Agent-to-agent discussion via NATS channels. Agents subscribe to channels, contribute to threads, and collaborate to answer user questions.</p>
						<InfoRow label="Enabled" value={policy.spec.a2a.enabled ? 'Yes' : 'No'} />
						{#if policy.spec.a2a.role}
							<InfoRow label="Role" value={policy.spec.a2a.role} />
						{/if}
						{#if policy.spec.a2a.subscribeChannels?.length}
							<InfoRow label="Channels" value={policy.spec.a2a.subscribeChannels.join(', ')} />
						{/if}
						{#if policy.spec.a2a.maxInferencesPerMinute}
							<InfoRow label="Max Inferences/min" value={policy.spec.a2a.maxInferencesPerMinute} />
						{/if}
						{#if policy.spec.a2a.discussionTimeoutSeconds}
							<InfoRow label="Discussion Timeout" value={`${policy.spec.a2a.discussionTimeoutSeconds}s`} />
						{/if}
						{#if policy.spec.a2a.skills?.length}
							<Section title="Skills ({policy.spec.a2a.skills.length})">
								{#each policy.spec.a2a.skills as skill}
									<InfoRow label={skill.id} value={skill.description || skill.name || ''} />
								{/each}
							</Section>
						{/if}
					</Section>
				{/if}

				{#if policy.spec.prompt?.system}
					<Section title="System Prompt">
						<pre class="prompt-text">{policy.spec.prompt.system}</pre>
					</Section>
				{/if}

				{#if policy.spec.inference}
					<Section title="Inference">
						{#if policy.spec.inference.temperature !== undefined}
							<InfoRow label="Temperature" value={policy.spec.inference.temperature} />
						{/if}
						{#if policy.spec.inference.topP !== undefined}
							<InfoRow label="Top P" value={policy.spec.inference.topP} />
						{/if}
						{#if policy.spec.inference.topK !== undefined}
							<InfoRow label="Top K" value={policy.spec.inference.topK} />
						{/if}
						{#if policy.spec.inference.maxTokens !== undefined}
							<InfoRow label="Max Tokens" value={policy.spec.inference.maxTokens} />
						{/if}
					</Section>
				{/if}

				<Section title="Status">
					<InfoRow label="Ready" value={policy.status?.ready ? 'Yes' : 'No'} />
					{#if policy.status?.message}
						<InfoRow label="Message" value={policy.status.message} />
					{/if}
					{#if policy.status?.lastUpdated}
						<InfoRow label="Last Updated" value={policy.status.lastUpdated} />
					{/if}
					{#if policy.status?.referencedBy?.length}
						<InfoRow label="Referenced By">
							{#snippet children()}
								<div class="ref-list">
									{#each policy?.status?.referencedBy ?? [] as agentName}
										<a href="{base}/agents/{agentName}?namespace={ns}" class="ref-link">{agentName}</a>
									{/each}
								</div>
							{/snippet}
						</InfoRow>
					{/if}
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

	.section-hint {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		margin: 0 0 0.75rem 0;
		line-height: 1.4;
	}

	.prompt-text {
		background: var(--color-bg);
		padding: 0.75rem;
		border-radius: 4px;
		font-size: 0.8rem;
		line-height: 1.5;
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 300px;
		overflow-y: auto;
		margin: 0;
	}

	.ref-list {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.ref-link {
		color: var(--color-primary);
		text-decoration: none;
		font-size: 0.85rem;
	}

	.ref-link:hover {
		text-decoration: underline;
	}
</style>
