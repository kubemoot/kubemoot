<script lang="ts">
	import { yesNo } from '$lib/resource-status';
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { resolve } from '$app/paths';
	import { namespace, refreshTrigger } from '$stores';
	import type { PromptModule, Agent } from '$types/kubemoot.js';

	let module = $state<PromptModule | null>(null);
	let referencingAgents = $state<Agent[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);

	const name = $derived($page.params.name as string);
	const ns = $derived($page.url.searchParams.get('namespace') || $namespace || 'kubemoot');

	async function fetchModule() {
		loading = true;
		error = null;
		try {
			const res = await fetch(`${resolve('/api/kubemoot/promptmodules/[name]', { name })}?namespace=${ns}`);
			const data = await res.json();
			if (data.error) throw new Error(data.error);
			module = data;
			fetchReferencingAgents(module?.metadata.namespace ?? ns, name);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch prompt module';
		} finally {
			loading = false;
		}
	}

	async function fetchReferencingAgents(agentNs: string, moduleName: string) {
		try {
			const res = await fetch(`${resolve('/api/kubemoot/agents')}?namespace=${encodeURIComponent(agentNs)}`);
			const data = await res.json();
			const items: Agent[] = data.items ?? [];
			// Agents reference PromptModules by name in spec.prompt.promptRefs (or
			// in some CRDs at spec.promptRefs depending on version). The dashboard
			// type uses spec.prompt.promptRefs, but defensively check both.
			referencingAgents = items.filter((a) => {
				const refs1 = (a.spec as { prompt?: { promptRefs?: string[] } }).prompt?.promptRefs ?? [];
				const refs2 = (a.spec as { promptRefs?: string[] }).promptRefs ?? [];
				return refs1.includes(moduleName) || refs2.includes(moduleName);
			});
		} catch {
			referencingAgents = [];
		}
	}

	onMount(fetchModule);

	$effect(() => {
		ns;
		$refreshTrigger;
		fetchModule();
	});
</script>

<div class="detail-header">
	<a href="{resolve('/promptmodules')}" class="back-link">&larr; Prompt Modules</a>
	{#if module}
		<h1>{module.metadata.name}</h1>
		<span class="order-badge">order {module.spec?.order ?? '-'}</span>
	{/if}
</div>

{#if loading}
	<p class="status-msg">Loading...</p>
{:else if error}
	<p class="status-msg error">{error}</p>
{:else if module}
	<div class="meta-row">
		<span><strong>Namespace:</strong> <code>{module.metadata.namespace}</code></span>
		<span><strong>Size:</strong> {(module.spec?.content?.length ?? 0).toLocaleString()} chars</span>
		<span><strong>Created:</strong> {module.metadata.creationTimestamp ? new Date(module.metadata.creationTimestamp).toLocaleString() : '-'}</span>
	</div>

	<h2>ADL Content</h2>
	<pre class="adl-content">{module.spec?.content ?? ''}</pre>

	<h2>Referenced By {referencingAgents.length > 0 ? `(${referencingAgents.length})` : ''}</h2>
	{#if referencingAgents.length === 0}
		<p class="muted">No agents in namespace <code>{ns}</code> reference this module via <code>spec.prompt.promptRefs</code>.</p>
	{:else}
		<table class="refs-table">
			<thead>
				<tr><th>Agent</th><th>Role</th><th>Ready</th></tr>
			</thead>
			<tbody>
				{#each referencingAgents as a (a.metadata.namespace + '/' + a.metadata.name)}
					<tr>
						<td><a class="mono link" href="{resolve('/agents/[name]', { name: a.metadata.name })}?namespace={a.metadata.namespace}">{a.metadata.name}</a></td>
						<td>{a.metadata.labels?.['kubemoot.ai/role'] ?? a.spec.type ?? '-'}</td>
						<td class:cond-true={a.status?.ready === true} class:cond-false={a.status?.ready === false}>
							{yesNo(a.status?.ready)}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
{/if}

<style>
	.detail-header {
		display: flex;
		align-items: center;
		gap: 1rem;
		margin-bottom: 1.25rem;
		flex-wrap: wrap;
	}
	.back-link {
		color: var(--color-text-muted);
		font-size: 0.85rem;
		text-decoration: none;
	}
	.back-link:hover { text-decoration: underline; }
	.detail-header h1 {
		font-size: 1.5rem;
		font-family: var(--font-mono);
		font-weight: 600;
	}

	.order-badge {
		font-size: 0.75rem;
		font-family: var(--font-mono);
		padding: 0.15rem 0.6rem;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		border: 1px solid var(--color-border);
	}

	.meta-row {
		display: flex;
		gap: 1.5rem;
		flex-wrap: wrap;
		font-size: 0.85rem;
		color: var(--color-text-muted);
		margin-bottom: 1.5rem;
	}
	.meta-row code {
		font-family: var(--font-mono);
		font-size: 0.78rem;
		color: var(--color-cyan);
	}

	h2 {
		font-size: 0.9rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
		margin-top: 1.5rem;
		margin-bottom: 0.75rem;
	}

	.adl-content {
		font-family: var(--font-mono);
		font-size: 0.8rem;
		line-height: 1.5;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 1rem 1.25rem;
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 60vh;
		overflow-y: auto;
		color: var(--color-text);
	}

	.status-msg { color: var(--color-text-muted); padding: 2rem; text-align: center; }
	.status-msg.error { color: var(--color-error); }
	.muted { color: var(--color-text-muted); font-size: 0.85rem; }

	.refs-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.85rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		overflow: hidden;
	}
	.refs-table th {
		text-align: left;
		padding: 0.55rem 0.9rem;
		color: var(--color-text-muted);
		font-weight: 500;
		font-size: 0.75rem;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		background: var(--color-bg-tertiary);
		border-bottom: 1px solid var(--color-border);
	}
	.refs-table td {
		padding: 0.55rem 0.9rem;
		border-bottom: 1px solid var(--color-border);
	}
	.refs-table tr:last-child td { border-bottom: none; }
	.mono { font-family: var(--font-mono); font-size: 0.8rem; }
	.link { color: var(--color-primary); text-decoration: none; }
	.link:hover { text-decoration: underline; }
	.cond-true { color: var(--color-success); }
	.cond-false { color: var(--color-error); }
</style>
