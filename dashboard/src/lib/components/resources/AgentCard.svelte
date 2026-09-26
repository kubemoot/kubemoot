<script lang="ts">
	import { base } from '$app/paths';
	import type { Agent, AgentHeartbeat } from '$types/kubemoot.js';

	interface Props {
		agent: Agent;
		showNamespace?: boolean;
		heartbeat?: AgentHeartbeat;
	}

	let { agent, showNamespace = false, heartbeat }: Props = $props();

	type AgentRole = 'coordinator' | 'tooler' | 'analyst' | 'researcher' | 'specialist' | 'system';

	function getRole(a: Agent): AgentRole {
		const labels = a.metadata.labels || {};
		const annotations = a.metadata.annotations || {};

		// Show the agent's real role from kubemoot.ai/role (the crews set
		// tooler/analyst/researcher/coordinator); "specialist" is kept only for
		// legacy crews that still emit it.
		const role = labels['kubemoot.ai/role'];
		if (role === 'coordinator') return 'coordinator';
		if (role === 'tooler') return 'tooler';
		if (role === 'analyst') return 'analyst';
		if (role === 'researcher') return 'researcher';
		if (role === 'specialist') return 'specialist';
		if (
			annotations['kubemoot.ai/onboarding-mode'] === 'true' ||
			annotations['kubemoot.ai/rtfm-mode'] === 'true'
		)
			return 'system';
		if (annotations['kubemoot.ai/discuss-role'] === 'observer') return 'system';
		if (!role) return 'system';
		return 'tooler';
	}

	const roleColors: Record<AgentRole, string> = {
		coordinator: 'var(--color-primary)',
		tooler: 'var(--color-success)',
		analyst: 'var(--color-cyan)',
		researcher: 'var(--color-purple)',
		specialist: 'var(--color-success)',
		system: 'var(--color-text-muted)'
	};

	const roleLabels: Record<AgentRole, string> = {
		coordinator: 'Coordinator',
		tooler: 'Tooler',
		analyst: 'Analyst',
		researcher: 'Researcher',
		specialist: 'Specialist',
		system: 'System'
	};

	const role = $derived(getRole(agent));

	function statusOf(a: Agent): 'success' | 'error' | 'pending' {
		if (a.status?.ready) return 'success';
		if (a.status?.phase === 'Error') return 'error';
		return 'pending';
	}

	const status = $derived(statusOf(agent));

	const description = $derived(agent.spec.description || '');
	const modelCount = $derived(agent.spec.models?.length || 0);
	const toolCount = $derived(() => {
		const enabledTools = agent.spec.deployment?.env?.find(e => e.name === 'KUBEMOOT_ENABLED_TOOLS');
		if (enabledTools?.value) return enabledTools.value.split(',').length;
		return agent.spec.mcpServers?.length || 0;
	});
	const ragCount = $derived(agent.spec.ragSources?.length || 0);

	type LivenessState = 'live' | 'degraded' | 'stale' | 'unknown';

	const liveness = $derived.by((): LivenessState => {
		if (!heartbeat) return 'unknown';
		const age = (Date.now() - new Date(heartbeat.timestamp).getTime()) / 1000;
		if (age > 300) return 'stale';
		if (!heartbeat.ollama || !heartbeat.nats) return 'degraded';
		if (age < 120) return 'live';
		return 'stale';
	});

	const livenessTooltip = $derived.by(() => {
		if (!heartbeat) return 'No heartbeat data';
		const age = Math.round((Date.now() - new Date(heartbeat.timestamp).getTime()) / 1000);
		const parts = [`Heartbeat: ${age}s ago`];
		parts.push(`Ollama: ${heartbeat.ollama ? 'reachable' : 'unreachable'}`);
		parts.push(`NATS: ${heartbeat.nats ? 'connected' : 'disconnected'}`);
		if (heartbeat.lastInference) {
			const infAge = Math.round((Date.now() - new Date(heartbeat.lastInference).getTime()) / 1000);
			parts.push(`Last inference: ${infAge}s ago`);
		}
		return parts.join('\n');
	});
</script>

<a href="{base}/agents/{agent.metadata.name}?namespace={agent.metadata.namespace}" class="card" style="--role-color: {roleColors[role]}">
	<header class="header">
		<div class="title-section">
			<div class="meta-row">
				<span class="role-badge" style="color: {roleColors[role]}; background-color: color-mix(in srgb, {roleColors[role]} 15%, transparent)">
					{roleLabels[role]}
				</span>
				{#if showNamespace && agent.metadata.namespace}
					<span class="namespace-badge">{agent.metadata.namespace}</span>
				{/if}
			</div>
			<h3 class="name">{agent.metadata.name}</h3>
		</div>
		<div class="status-indicators">
			{#if liveness !== 'unknown'}
				<span class="liveness-dot {liveness}" title={livenessTooltip}></span>
			{/if}
			<span class="status-dot {status}"></span>
		</div>
	</header>

	{#if description}
		<p class="description">{description}</p>
	{/if}

	<footer class="footer">
		<div class="resources">
			<span class="resource-badge" class:active={modelCount > 0}>
				{modelCount} model{modelCount !== 1 ? 's' : ''}
			</span>
			<span class="resource-badge" class:active={toolCount() > 0}>
				{toolCount()} tool{toolCount() !== 1 ? 's' : ''}
			</span>
			<span class="resource-badge" class:active={ragCount > 0}>
				{ragCount} RAG
			</span>
		</div>
	</footer>
</a>

<style>
	.card {
		display: flex;
		flex-direction: column;
		background-color: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-left: 3px solid var(--role-color);
		border-radius: 0.5rem;
		padding: 1rem;
		transition: all 0.15s;
		text-decoration: none;
		color: inherit;
	}

	.card:hover {
		border-color: var(--color-primary);
		border-left-color: var(--role-color);
		background-color: rgba(30, 41, 59, 0.8);
		text-decoration: none;
	}

	.header {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
	}

	.title-section {
		min-width: 0;
		flex: 1;
	}

	.meta-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.role-badge {
		font-size: 0.65rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
	}

	.namespace-badge {
		font-size: 0.65rem;
		font-weight: 500;
		color: var(--color-primary);
		background-color: rgba(59, 130, 246, 0.15);
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
	}

	.name {
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text);
		margin-top: 0.25rem;
		word-break: break-word;
	}

	.status-indicators {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		margin-top: 0.375rem;
	}

	.liveness-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.liveness-dot.live {
		background-color: #10b981;
		animation: pulse-live 2s ease-in-out infinite;
	}

	.liveness-dot.degraded {
		background-color: #f59e0b;
	}

	.liveness-dot.stale {
		background-color: #6b7280;
	}

	.status-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.status-dot.success {
		background-color: var(--color-success);
	}

	.status-dot.error {
		background-color: var(--color-error);
	}

	.status-dot.pending {
		background-color: var(--color-primary);
		animation: pulse 1.5s ease-in-out infinite;
	}

	.description {
		margin-top: 0.75rem;
		font-size: 0.8rem;
		color: var(--color-text-muted);
		line-height: 1.4;
		display: -webkit-box;
		-webkit-line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}

	.footer {
		margin-top: auto;
		padding-top: 0.75rem;
		border-top: 1px solid var(--color-border);
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.description + .footer {
		margin-top: 0.75rem;
	}

	.resources {
		display: flex;
		gap: 0.5rem;
	}

	.resource-badge {
		background-color: var(--color-bg-tertiary);
		padding: 0.125rem 0.5rem;
		border-radius: 0.25rem;
		font-size: 0.7rem;
		color: var(--color-text-muted);
	}

	.resource-badge.active {
		background-color: rgba(59, 130, 246, 0.15);
		color: var(--color-primary);
	}

	@keyframes pulse {
		0%, 100% {
			opacity: 1;
		}
		50% {
			opacity: 0.4;
		}
	}

	@keyframes pulse-live {
		0%, 100% {
			opacity: 1;
			box-shadow: 0 0 0 0 rgba(16, 185, 129, 0.4);
		}
		50% {
			opacity: 0.8;
			box-shadow: 0 0 0 3px rgba(16, 185, 129, 0);
		}
	}
</style>
