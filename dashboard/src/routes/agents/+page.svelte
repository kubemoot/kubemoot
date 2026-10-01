<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { namespace, refreshTrigger } from '$stores';
	import { AgentCard } from '$components/resources';
	import { HelpTooltip } from '$components/common';
	import type { Agent, AgentHeartbeat } from '$types/kubemoot.js';
	import { LiveList } from '$lib/client/liveList.svelte';
	import { agentStateFor } from '$lib/crewScope';
	import { agentRole, type AgentRole } from '$lib/agent-role';

	// Agent list is push-based (watch→SSE). Heartbeats are decorative NATS-KV
	// liveness and stay on the (15s) global poll. The bucket is global; its keys are
	// `<namespace>.<agent>`, so each card looks up its own namespace and name.
	const live = new LiveList<Agent>('agents');
	let heartbeats = $state<Record<string, AgentHeartbeat>>({});

	interface RoleSection {
		role: AgentRole;
		label: string;
		color: string;
		agents: Agent[];
	}

	const sections = $derived.by((): RoleSection[] => {
		const roleOrder: Array<{ role: AgentRole; label: string; color: string }> = [
			{ role: 'coordinator', label: 'Coordinator', color: 'var(--color-primary)' },
			{ role: 'tooler', label: 'Toolers', color: 'var(--color-success)' },
			{ role: 'analyst', label: 'Analysts', color: 'var(--color-cyan)' },
			{ role: 'researcher', label: 'Researchers', color: 'var(--color-purple)' },
			{ role: 'specialist', label: 'Specialists', color: 'var(--color-success)' },
			{ role: 'system', label: 'System', color: 'var(--color-text-muted)' }
		];

		const grouped = new Map<AgentRole, Agent[]>();
		for (const agent of live.items) {
			const role = agentRole(agent);
			if (!grouped.has(role)) grouped.set(role, []);
			grouped.get(role)!.push(agent);
		}

		return roleOrder
			.map((r) => ({ ...r, agents: grouped.get(r.role) || [] }))
			.filter((s) => s.agents.length > 0);
	});

	async function fetchHeartbeats() {
		try {
			const hbRes = await fetch(resolve('/api/nats/kv'));
			if (!hbRes.ok) return;
			const hbData = await hbRes.json();
			heartbeats = hbData.agents || {};
		} catch {
			// non-critical
		}
	}

	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});
	// Heartbeats refresh on the global tick (NATS-KV, global scope).
	$effect(() => {
		$refreshTrigger;
		fetchHeartbeats();
	});
</script>

<div class="agents-page">
	<header class="page-header">
		<div class="title-row">
			<h2 class="title">Agents</h2>
			<HelpTooltip text="Agents are AI assistants backed by LLM models that can use MCP tools and RAG knowledge sources. Each agent runs as a Kubernetes deployment." />
			{#if live.items.length > 0}
				<span class="count">{live.items.length}</span>
			{/if}
		</div>
	</header>

	<div class="content">
		{#if live.loading}
			<div class="center-message">
				<div class="spinner"></div>
				<span>Loading...</span>
			</div>
		{:else if live.error}
			<div class="center-message error-message">
				<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<circle cx="12" cy="12" r="10" />
					<line x1="12" y1="8" x2="12" y2="12" />
					<line x1="12" y1="16" x2="12.01" y2="16" />
				</svg>
				<span>{live.error}</span>
			</div>
		{:else if live.items.length === 0}
			<div class="center-message">
				{#if $namespace === ''}
					<p>No agents found in any namespace.</p>
				{:else}
					<p>No agents found in namespace <code>{$namespace}</code>.</p>
				{/if}
			</div>
		{:else}
			{#each sections as section}
				<div class="section">
					<div class="section-header">
						<span class="section-bar" style="background-color: {section.color}"></span>
						<h3 class="section-title">{section.label}</h3>
						<span class="section-count">{section.agents.length}</span>
					</div>
					<div class="grid">
						{#each section.agents as agent (agent.metadata.namespace + '/' + agent.metadata.name)}
							<AgentCard {agent} showNamespace={$namespace === ''} heartbeat={agentStateFor(heartbeats, agent.metadata.namespace, agent.metadata.name)} />
						{/each}
					</div>
				</div>
			{/each}
		{/if}
	</div>
</div>

<style>
	.agents-page {
		display: flex;
		flex-direction: column;
		height: 100%;
	}

	.page-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 1.5rem;
	}

	.title-row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.title {
		font-size: 1.5rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.count {
		background-color: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		padding: 0.25rem 0.625rem;
		border-radius: 999px;
		font-size: 0.8rem;
		font-weight: 500;
	}

	.content {
		flex: 1;
		overflow-y: auto;
	}

	.section {
		margin-bottom: 2rem;
	}

	.section:last-child {
		margin-bottom: 0;
	}

	.section-header {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 0.75rem;
	}

	.section-bar {
		width: 3px;
		height: 16px;
		border-radius: 2px;
	}

	.section-title {
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--color-text);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}

	.section-count {
		font-size: 0.7rem;
		color: var(--color-text-muted);
		background-color: var(--color-bg-tertiary);
		padding: 0.1rem 0.4rem;
		border-radius: 999px;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
		gap: 1rem;
	}

	.center-message {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 1rem;
		padding: 4rem 2rem;
		color: var(--color-text-muted);
		text-align: center;
	}

	.error-message {
		color: var(--color-error);
	}

	.spinner {
		width: 32px;
		height: 32px;
		border: 3px solid var(--color-bg-tertiary);
		border-top-color: var(--color-primary);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
