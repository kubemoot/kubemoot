<script lang="ts">
	import { readinessStatus } from '#lib/resource-status.js';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { namespace, refreshTrigger } from '#lib/stores/index.js';
	import { DetailPanel } from '#lib/components/layout/index.js';
	import { Section, InfoRow, StatusBadge } from '#lib/components/common/index.js';
	import type { Agent, AgentHeartbeat, MCPServer, MCPTool } from '#lib/types/kubemoot.js';
	import { agentStateFor } from '#lib/crewScope.js';
	import { splitCamelCase } from '#lib/text-utils.js';
	import { mcpServerNames, mcpServerRefs, visibleTools } from '#lib/agent-detail.js';
	import { LIVENESS_BADGE, heartbeatLiveness, secondsSince } from '#lib/agent-liveness.js';

	let agent = $state<Agent | null>(null);
	let heartbeat = $state<AgentHeartbeat | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let mcpServerTools = $state<Map<string, MCPTool[]>>(new Map());
	let assembledPrompt = $state<string | null>(null);
	let promptConfigMapName = $state<string | null>(null);
	let promptError = $state<string | null>(null);
	let showAssembledPrompt = $state(false);

	const name = $derived(page.params.name as string);
	const ns = $derived(page.url.searchParams.get('namespace') || $namespace);

	async function fetchAgent() {
		loading = true;
		error = null;

		try {
			const [res, hbRes] = await Promise.all([
				fetch(`${resolve('/api/kubemoot/agents/[name]', { name })}?namespace=${ns}`),
				fetch(resolve('/api/nats/kv')).catch(() => null)
			]);
			if (!res.ok) throw new Error('Agent not found');
			agent = await res.json();

			if (hbRes?.ok && name) await loadHeartbeat(hbRes, name);

			// Fetch tools for each referenced MCP server
			const serverNames = mcpServerNames(agent);
			if (serverNames.length > 0) {
				await fetchMCPServerTools(serverNames);
			}

			// Fetch assembled system prompt from the agent's policy ConfigMap
			fetchAssembledPrompt();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch agent';
		} finally {
			loading = false;
		}
	}

	async function loadHeartbeat(hbRes: Response, agentName: string) {
		const hbData = await hbRes.json();
		heartbeat =
			agentStateFor<AgentHeartbeat>(hbData.agents, agent?.metadata.namespace ?? ns, agentName) ?? null;
	}

	async function fetchAssembledPrompt() {
		assembledPrompt = null;
		promptConfigMapName = null;
		promptError = null;
		try {
			const res = await fetch(`${resolve('/api/kubemoot/agents/[name]/prompt', { name })}?namespace=${ns}`);
			const data = await res.json();
			if (!res.ok) {
				promptError = data.error ?? 'Failed to load assembled prompt';
				promptConfigMapName = data.configMapName ?? null;
				return;
			}
			promptConfigMapName = data.configMapName ?? null;
			assembledPrompt = data.systemPrompt ?? null;
		} catch (e) {
			promptError = e instanceof Error ? e.message : 'Failed to load assembled prompt';
		}
	}

	const promptRefs = $derived<string[]>(
		((agent?.spec as { prompt?: { promptRefs?: string[] } } | undefined)?.prompt?.promptRefs) ??
			((agent?.spec as { promptRefs?: string[] } | undefined)?.promptRefs) ??
			[]
	);

	async function fetchMCPServerTools(serverNames: string[]) {
		const toolMap = new Map<string, MCPTool[]>();
		await Promise.all(
			serverNames.map(async (serverName) => {
				try {
					const res = await fetch(`${resolve('/api/kubemoot/mcpservers/[name]', { name: serverName })}?namespace=${ns}`);
					if (res.ok) {
						const server: MCPServer = await res.json();
						toolMap.set(serverName, server.status?.tools || []);
					}
				} catch {
					// Skip servers that can't be fetched
				}
			})
		);
		mcpServerTools = toolMap;
	}

	onMount(() => {
		fetchAgent();
	});

	$effect(() => {
		ns;
		$refreshTrigger;
		fetchAgent();
	});

	const status = $derived(readinessStatus(agent?.status));

	const statusLabel = $derived(splitCamelCase(agent?.status?.phase || 'Unknown'));

	// MCP servers: prefer spec refs, fall back to status (gateway agents)
	const mcpServers = $derived(mcpServerRefs(agent));

	// Enabled tools from KUBEMOOT_ENABLED_TOOLS env var (for gateway-mode agents with no spec.mcpServers)
	const enabledToolNames = $derived(() => {
		const envVar = agent?.spec.deployment?.env?.find((e: { name: string; value?: string }) => e.name === 'KUBEMOOT_ENABLED_TOOLS');
		if (envVar?.value) return envVar.value.split(',').map((t: string) => t.trim());
		return [];
	});
</script>

<DetailPanel title={name} subtitle="Agent" {loading} {error}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">Agent</span>
				<h1 class="title">{name}</h1>
			</div>
			{#if agent}
				<StatusBadge {status} label={statusLabel} />
			{/if}
		</div>
	{/snippet}

	{#snippet children()}
		{#if agent}
			<div class="sections">
				<Section title="Spec">
					<InfoRow label="Type" value={agent.spec.type || 'chat'} />
					<InfoRow label="Description" value={agent.spec.description} />
					{#if agent?.spec?.guardrails}
						<InfoRow label="Guardrails">
							{#snippet children()}
								<span class="inline-meta">
									{#if agent?.spec?.guardrails?.maxToolCallsPerTurn}max {agent.spec.guardrails.maxToolCallsPerTurn} tools/turn{/if}
									{#if agent?.spec?.guardrails?.auditLog} | audit{/if}
								</span>
							{/snippet}
						</InfoRow>
					{/if}
					{#if agent?.spec?.a2a}
						<InfoRow label="A2A Role" value={agent.spec.a2a.role || 'peer'} />
						{#if agent?.spec?.a2a?.subscribeChannels?.length}
							<InfoRow label="Channels" value={agent.spec.a2a.subscribeChannels.join(', ')} />
						{/if}
					{/if}
				</Section>

				<Section title="Prompt ({promptRefs.length} module{promptRefs.length === 1 ? '' : 's'})">
					{#if promptRefs.length > 0}
						<div class="prompt-refs">
							{#each promptRefs as ref}
								<a class="prompt-ref-chip" href="{resolve('/promptmodules/[name]', { name: ref })}?namespace={ns}">{ref}</a>
							{/each}
						</div>
					{:else}
						<p class="prompt-empty">No promptRefs declared on this Agent.</p>
					{/if}

					{#if promptError}
						<p class="prompt-error">
							Assembled prompt ConfigMap <code class="mono">{promptConfigMapName ?? '(unknown)'}</code> not readable: {promptError}
						</p>
					{:else if assembledPrompt !== null}
						<div class="prompt-assembled-row">
							<button class="link-button" type="button" onclick={() => (showAssembledPrompt = !showAssembledPrompt)}>
								{showAssembledPrompt ? 'Hide' : 'Show'} assembled system prompt ({assembledPrompt.length.toLocaleString()} chars)
							</button>
							<span class="prompt-source mono">from ConfigMap <code>{promptConfigMapName}</code>/system.txt</span>
						</div>
						{#if showAssembledPrompt}
							<pre class="prompt-assembled">{assembledPrompt}</pre>
						{/if}
					{/if}
				</Section>

				<Section title="Models ({agent.spec.models?.length || 0})">
					{#if agent.spec.models}
						{#each agent.spec.models as model}
							<div class="ref-item">
								<InfoRow label={model.name}>
									{#snippet children()}
										<span class="role-badge">{model.role || 'primary'}</span>
									{/snippet}
								</InfoRow>
							</div>
						{/each}
					{/if}
				</Section>

				{#if mcpServers.length > 0}
					<Section title="MCP Servers ({mcpServers.length})">
						{#each mcpServers as mcp}
							{@const mcpStatus = agent.status?.mcpServerStatus?.find((s) => s.name === mcp.name)}
							{@const specRef = agent.spec.mcpServers?.find((s) => s.name === mcp.name)}
							{@const allTools = mcpServerTools.get(mcp.name) || []}
							{@const shownTools = visibleTools(allTools, specRef)}
							<div class="mcp-server-block">
								<div class="mcp-server-header">
									<a href="{resolve('/mcpservers/[name]', { name: mcp.name })}?namespace={ns}" class="mcp-server-link">{mcp.name}</a>
									{#if mcpStatus}
										<StatusBadge
											status={mcpStatus.ready ? 'success' : 'error'}
											label={mcpStatus.ready ? 'Ready' : 'Not Ready'}
											size="sm"
										/>
										{#if mcpStatus.toolCount}
											<span class="tool-count-badge">{mcpStatus.toolCount} tools</span>
										{/if}
									{/if}
								</div>
								{#if specRef?.enabledTools && specRef.enabledTools.length > 0}
									<div class="tool-filter">Enabled: {specRef.enabledTools.join(', ')}</div>
								{/if}
								{#if specRef?.disabledTools && specRef.disabledTools.length > 0}
									<div class="tool-filter disabled">Disabled: {specRef.disabledTools.join(', ')}</div>
								{/if}
								{#if shownTools.length > 0}
									<div class="tools-list">
										{#each shownTools as tool}
											<div class="tool">
												<span class="tool-name">{tool.name}</span>
												{#if tool.description}
													<span class="tool-desc">{tool.description}</span>
												{/if}
											</div>
										{/each}
									</div>
								{/if}
							</div>
						{/each}
					</Section>
				{/if}

					{#if mcpServers.length === 0 && enabledToolNames().length > 0}
					<Section title="Enabled Tools ({enabledToolNames().length})">
						<div class="tools-list">
							{#each enabledToolNames() as toolName}
								<div class="tool">
									<span class="tool-name">{toolName}</span>
								</div>
							{/each}
						</div>
					</Section>
				{/if}

				{#if agent.spec.ragSources && agent.spec.ragSources.length > 0}
					<Section title="RAG Sources ({agent.spec.ragSources.length})">
						{#each agent.spec.ragSources as rag}
							{@const ragStatus = agent.status?.ragSourceStatus?.find((s) => s.name === rag.name)}
							<InfoRow label={rag.name}>
								{#snippet children()}
									<div class="rag-meta">
										<span>topK: {rag.topK || 5}</span>
										{#if ragStatus}
											<StatusBadge
												status={ragStatus.ready ? 'success' : 'error'}
												label={ragStatus.ready ? 'Ready' : 'Not Ready'}
												size="sm"
											/>
											{#if ragStatus.documentCount}
												<span class="doc-count">{ragStatus.documentCount} docs</span>
											{/if}
										{/if}
									</div>
								{/snippet}
							</InfoRow>
						{/each}
					</Section>
				{/if}

				{#if agent.spec.inference}
					<Section title="Inference">
						<InfoRow label="Temperature" value={agent.spec.inference.temperature} />
						<InfoRow label="Top P" value={agent.spec.inference.topP} />
						<InfoRow label="Top K" value={agent.spec.inference.topK} />
						<InfoRow label="Max Tokens" value={agent.spec.inference.maxTokens} />
					</Section>
				{/if}

				<Section title="Status">
					<InfoRow label="Ready" value={agent.status?.ready ? 'Yes' : 'No'} />
					<InfoRow label="Phase" value={statusLabel} />
					<InfoRow label="Endpoint" value={agent.status?.endpoint} mono />
					<InfoRow label="Replicas" value={`${agent.status?.availableReplicas || 0}/${agent.spec.deployment?.replicas || 1}`} />
					<InfoRow label="Message" value={agent.status?.message} />
				</Section>

				{#if heartbeat}
				{@const hbAge = secondsSince(heartbeat.timestamp)}
				{@const liveness = LIVENESS_BADGE[heartbeatLiveness(hbAge, heartbeat)]}
				<Section title="Runtime Health">
					<InfoRow label="Liveness">
						{#snippet children()}
							<StatusBadge
								status={liveness.status}
								label={liveness.label}
								size="sm"
							/>
						{/snippet}
					</InfoRow>
					<InfoRow label="NATS Connected" value={heartbeat.nats ? 'Yes' : 'No'} />
					<InfoRow label="Ollama Reachable" value={heartbeat.ollama ? 'Yes' : 'No'} />
					<InfoRow label="Model" value={heartbeat.model} mono />
					<InfoRow label="Heartbeat Age" value={`${hbAge}s`} />
					{#if heartbeat.lastInference}
						{@const infAge = secondsSince(heartbeat.lastInference)}
						<InfoRow label="Last Inference" value={`${infAge}s ago`} />
					{/if}
				</Section>
			{/if}

			{#if agent.status?.modelStatus && agent.status.modelStatus.length > 0}
					<Section title="Model Status">
						{#each agent.status.modelStatus as ms}
							<InfoRow label={ms.name}>
								{#snippet children()}
									<StatusBadge
										status={ms.ready ? 'success' : 'error'}
										label={ms.ready ? 'Ready' : 'Not Ready'}
										size="sm"
									/>
								{/snippet}
							</InfoRow>
						{/each}
					</Section>
				{/if}

				<Section title="Metadata" collapsible defaultOpen={false}>
					<InfoRow label="Namespace" value={agent.metadata.namespace} />
					<InfoRow label="UID" value={agent.metadata.uid} mono />
					<InfoRow label="Created" value={agent.metadata.creationTimestamp} />
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

	.role-badge {
		background-color: var(--color-bg-tertiary);
		padding: 0.125rem 0.5rem;
		border-radius: 0.25rem;
		font-size: 0.75rem;
		text-transform: capitalize;
	}

	.inline-meta {
		font-size: 0.8rem;
		color: var(--color-text-muted);
	}

	.mcp-server-block {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0.75rem;
		background: var(--color-bg-tertiary);
		border-radius: 0.375rem;
		margin-bottom: 0.5rem;
	}

	.mcp-server-header {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.mcp-server-link {
		color: var(--color-primary);
		text-decoration: none;
		font-weight: 600;
		font-size: 0.85rem;
	}

	.mcp-server-link:hover {
		text-decoration: underline;
	}

	.tool-count-badge {
		font-size: 0.7rem;
		color: var(--color-text-muted);
		background: var(--color-bg-secondary);
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
	}

	.tool-filter {
		font-size: 0.7rem;
		color: #10b981;
		font-family: var(--font-mono);
	}

	.tool-filter.disabled {
		color: #f87171;
	}

	.tools-list {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.tool {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		padding: 0.375rem 0.5rem;
		background: var(--color-bg-secondary);
		border-radius: 0.25rem;
	}

	.tool-name {
		font-family: var(--font-mono);
		font-size: 0.8rem;
		font-weight: 500;
		color: var(--color-text);
	}

	.tool-desc {
		font-size: 0.7rem;
		color: var(--color-text-muted);
	}

	.rag-meta {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		font-size: 0.8rem;
	}

	.doc-count {
		font-size: 0.7rem;
		color: var(--color-text-muted);
	}

	.prompt-refs {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		margin-bottom: 0.75rem;
	}

	.prompt-ref-chip {
		font-family: var(--font-mono);
		font-size: 0.78rem;
		padding: 0.2rem 0.6rem;
		border-radius: 0.25rem;
		background: var(--color-bg-tertiary);
		color: var(--color-primary);
		border: 1px solid var(--color-border);
		text-decoration: none;
	}
	.prompt-ref-chip:hover {
		border-color: var(--color-primary);
		text-decoration: none;
	}

	.prompt-empty {
		color: var(--color-text-muted);
		font-size: 0.85rem;
		margin: 0 0 0.75rem 0;
	}

	.prompt-error {
		color: var(--color-text-muted);
		font-size: 0.8rem;
		margin: 0 0 0.5rem 0;
	}

	.prompt-error code {
		color: var(--color-cyan);
		font-family: var(--font-mono);
		font-size: 0.75rem;
	}

	.prompt-assembled-row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex-wrap: wrap;
		margin-top: 0.5rem;
	}

	.link-button {
		background: none;
		border: none;
		padding: 0;
		color: var(--color-primary);
		font: inherit;
		font-size: 0.85rem;
		cursor: pointer;
		text-decoration: none;
	}
	.link-button:hover { text-decoration: underline; }

	.prompt-source {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
	.prompt-source code {
		font-family: var(--font-mono);
		color: var(--color-cyan);
	}

	.prompt-assembled {
		font-family: var(--font-mono);
		font-size: 0.78rem;
		line-height: 1.5;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 0.375rem;
		padding: 0.75rem 1rem;
		margin-top: 0.5rem;
		max-height: 50vh;
		overflow-y: auto;
		white-space: pre-wrap;
		word-break: break-word;
		color: var(--color-text);
	}
</style>
