<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/stores';
	import type { Crew, Agent } from '$types/kubemoot.js';
	import { chartName, chartVersion } from '$lib/crew-chart';
	import { crewDisplayName, crewTechnicalHint } from '$lib/crew-display-name';
	import { NO_VALUE, yesNo } from '$lib/resource-status';

	// The per-crew resume model (embedding-based subcommittee selection): the
	// operator-managed RAGSource `crew-<crew>-resumes`. We surface its content
	// hash (which agent set it was built from) and the timestamp it was last
	// embedded, so the resume model's freshness is visible on the crew page.
	interface ResumeModel {
		spec?: {
			embeddingModelRef?: string;
			vectorStore?: { collection?: string; collectionName?: string };
			source?: { natsKV?: { contentHash?: string; bucket?: string; key?: string } };
		};
		status?: {
			phase?: string;
			queryEndpoint?: string;
			indexingStats?: { lastIndexed?: string; duration?: string; documentCount?: number };
		};
	}

	let crew = $state<Crew | null>(null);
	let agents = $state<Agent[]>([]);
	let resumeModel = $state<ResumeModel | null>(null);
	let loading = $state(true);
	let agentsLoading = $state(false);
	let error = $state<string | null>(null);

	$effect(() => {
		const name = $page.params.name as string;
		const ns = $page.url.searchParams.get('namespace') || 'kubemoot';
		if (name) fetchCrew(ns, name);
	});

	async function fetchCrew(ns: string, name: string) {
		loading = true;
		error = null;
		try {
			const res = await fetch(`${resolve('/api/kubemoot/crews/[name]', { name })}?namespace=${ns}`);
			const data = await res.json();
			if (data.error) throw new Error(data.error);
			crew = data;
			loadCrewChildren(crew?.metadata.namespace ?? ns, crew?.metadata.name ?? name);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch crew';
		} finally {
			loading = false;
		}
	}

	function loadCrewChildren(crewNs: string, crewName: string) {
		fetchAgents(crewNs, crewName);
		fetchResumeModel(crewNs, crewName);
	}

	async function fetchAgents(agentNs: string, crewName: string) {
		agentsLoading = true;
		try {
			const res = await fetch(`${resolve('/api/kubemoot/agents')}?namespace=${encodeURIComponent(agentNs)}`);
			const data = await res.json();
			const items: Agent[] = data.items ?? [];
			agents = items.filter((a) => {
				const label = a.metadata.labels?.['kubemoot.ai/crew'];
				return !label || label === crewName;
			});
		} catch {
			agents = [];
		} finally {
			agentsLoading = false;
		}
	}

	async function fetchResumeModel(ns: string, crewName: string) {
		// Resume RAGSource is operator-named `crew-<crew>-resumes`. Absent for
		// crews without a coordinator/resume pipeline - treat 404/error as "none".
		try {
			const rs = `crew-${crewName}-resumes`;
			const res = await fetch(`${resolve('/api/kubemoot/ragsources/[name]', { name: rs })}?namespace=${encodeURIComponent(ns)}`);
			const data = await res.json();
			resumeModel = data?.error ? null : data;
		} catch {
			resumeModel = null;
		}
	}

	function shortHash(h?: string): string {
		return h ? h.slice(0, 12) : '-';
	}

	function fmtTimestamp(ts?: string): string {
		return ts ? new Date(ts).toLocaleString() : '-';
	}

	function agentRoleLabel(a: Agent): string {
		return a.metadata.labels?.['kubemoot.ai/role'] ?? a.spec.type ?? '-';
	}
</script>

<svelte:head>
	{#if crew}
		<title>{crewDisplayName(crew)} - Crews - Kubemoot Dashboard</title>
	{/if}
</svelte:head>

<div class="detail-header">
	<a href="{resolve('/crews')}" class="back-link">&larr; Crews</a>
	{#if crew}
		{@const version = chartVersion(crew.metadata.labels)}
		{@const technical = crewTechnicalHint(crew)}
		<h1 title={technical}>{crewDisplayName(crew)}</h1>
		{#if technical}
			<span class="technical-name">{technical}</span>
		{/if}
		{#if version}
			<span class="chart-badge" title="Helm chart version (reflects the applied chart, not the desired chart in the HelmRelease)">v{version}</span>
		{/if}
		<span class="phase-badge" class:ready={crew.status?.phase === 'Ready'} class:err={crew.status?.phase === 'Error'}>
			{crew.status?.phase ?? 'Unknown'}
		</span>
	{/if}
</div>

{#if loading}
	<p class="status-msg">Loading...</p>
{:else if error}
	<p class="status-msg error">{error}</p>
{:else if crew}
	<div class="detail-grid">
		<div class="detail-section">
			<h2>Spec</h2>
			<table class="detail-table"><tbody>
				<tr><th>Description</th><td>{crew.spec.description || '-'}</td></tr>
				<tr><th>Discussion Enabled</th><td>{crew.spec.discussion?.enabled !== false ? 'Yes' : 'No'}</td></tr>
			</tbody></table>
		</div>

		<div class="detail-section">
			<h2>Status</h2>
			<table class="detail-table"><tbody>
				<tr><th>Phase</th><td>{crew.status?.phase ?? '-'}</td></tr>
				<tr><th>Ready</th><td>{crew.status?.ready ? 'Yes' : 'No'}</td></tr>
				<tr><th>Agents</th><td>{crew.status?.agentCount ?? '-'}</td></tr>
				<tr><th>Coordinator</th><td class="mono">{crew.status?.coordinatorRef || '-'}</td></tr>
				<tr><th>Discussion Endpoint</th><td class="mono">{crew.status?.discussionEndpoint || '-'}</td></tr>
				{#if crew.status?.message}
					<tr><th>Message</th><td>{crew.status.message}</td></tr>
				{/if}
			</tbody></table>
		</div>

		<div class="detail-section full">
			<h2>Resume Model</h2>
			{#if resumeModel}
				<table class="detail-table"><tbody>
					<tr><th>Collection</th><td class="mono">{resumeModel.spec?.vectorStore?.collection ?? '-'}</td></tr>
					<tr><th>Content Hash</th><td class="mono" title={resumeModel.spec?.source?.natsKV?.contentHash ?? ''}>{shortHash(resumeModel.spec?.source?.natsKV?.contentHash)}</td></tr>
					<tr><th>Last Embedded</th><td>{fmtTimestamp(resumeModel.status?.indexingStats?.lastIndexed)}</td></tr>
					<tr><th>Phase</th><td>{resumeModel.status?.phase ?? '-'}</td></tr>
					<tr><th>Embedding Model</th><td class="mono">{resumeModel.spec?.embeddingModelRef ?? '-'}</td></tr>
					<tr><th>Query Service</th><td class="mono">{resumeModel.status?.queryEndpoint ?? '-'}</td></tr>
				</tbody></table>
			{:else}
				<p class="status-msg">No resume model - this crew has no coordinator-driven resume collection.</p>
			{/if}
		</div>

		<div class="detail-section full">
			<h2>Helm Chart</h2>
			<table class="detail-table"><tbody>
				<tr><th>Chart</th><td class="mono">{chartName(crew.metadata.labels) ?? NO_VALUE}</td></tr>
				<tr><th>Version</th><td class="mono">{chartVersion(crew.metadata.labels) ?? NO_VALUE}</td></tr>
				<tr><th>Managed By</th><td class="mono">{crew.metadata.labels?.['app.kubernetes.io/managed-by'] ?? '-'}</td></tr>
			</tbody></table>
		</div>

		<div class="detail-section full">
			<h2>Metadata</h2>
			<table class="detail-table"><tbody>
				<tr><th>Namespace</th><td class="mono">{crew.metadata.namespace}</td></tr>
				<tr><th>UID</th><td class="mono uid">{crew.metadata.uid}</td></tr>
				<tr><th>Created</th><td>{crew.metadata.creationTimestamp ? new Date(crew.metadata.creationTimestamp).toLocaleString() : '-'}</td></tr>
			</tbody></table>
		</div>
	</div>

	<div class="detail-section full">
		<h2>Agents <span class="muted">({agents.length})</span></h2>
		{#if agentsLoading}
			<p class="muted small">Loading agents...</p>
		{:else if agents.length === 0}
			<p class="muted small">No agents found in <code class="mono">{crew.metadata.namespace}</code></p>
		{:else}
			<table class="agents-table">
				<thead>
					<tr><th>Name</th><th>Role</th><th>Type</th><th>Phase</th><th>Ready</th><th>Endpoint</th></tr>
				</thead>
				<tbody>
					{#each agents as a (a.metadata.namespace + '/' + a.metadata.name)}
						<tr>
							<td>
								<a class="mono" href="{resolve('/agents/[name]', { name: a.metadata.name })}?namespace={a.metadata.namespace}">{a.metadata.name}</a>
							</td>
							<td>{agentRoleLabel(a)}</td>
							<td>{a.spec.type ?? '-'}</td>
							<td>{a.status?.phase ?? '-'}</td>
							<td class:cond-true={a.status?.ready === true} class:cond-false={a.status?.ready === false}>
								{yesNo(a.status?.ready)}
							</td>
							<td class="mono small">{a.status?.endpoint ?? '-'}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
	</div>

	{#if crew.status?.conditions && crew.status.conditions.length > 0}
		<div class="detail-section full">
			<h2>Conditions</h2>
			<table class="conditions-table">
				<thead>
					<tr><th>Type</th><th>Status</th><th>Reason</th><th>Message</th><th>Time</th></tr>
				</thead>
				<tbody>
					{#each crew.status.conditions as cond}
						<tr>
							<td>{cond.type}</td>
							<td class:cond-true={cond.status === 'True'} class:cond-false={cond.status === 'False'}>{cond.status}</td>
							<td>{cond.reason}</td>
							<td>{cond.message}</td>
							<td>{cond.lastTransitionTime ? new Date(cond.lastTransitionTime).toLocaleString() : '-'}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
{/if}

<style>
	.detail-header {
		display: flex; align-items: center; gap: 1rem; margin-bottom: 1.5rem;
	}
	.back-link { color: var(--color-text-muted); font-size: 0.85rem; }
	.detail-header h1 { font-size: 1.5rem; font-family: var(--font-mono); }
	.technical-name { font-size: 0.85rem; font-family: var(--font-mono); color: var(--color-text-muted); }
	.phase-badge {
		font-size: 0.75rem; font-weight: 600; padding: 0.2rem 0.6rem;
		border-radius: 999px; text-transform: uppercase;
		background: var(--color-bg-tertiary); color: var(--color-text-muted);
	}
	.phase-badge.ready { background: rgba(34, 197, 94, 0.15); color: var(--color-success); }
	.phase-badge.err { background: rgba(239, 68, 68, 0.15); color: var(--color-error); }

	.chart-badge {
		font-size: 0.75rem; font-weight: 500; padding: 0.2rem 0.6rem;
		border-radius: 999px; font-family: var(--font-mono);
		background: var(--color-bg-tertiary); color: var(--color-text-muted);
		border: 1px solid var(--color-border);
	}

	.muted { color: var(--color-text-muted); font-weight: 400; }
	.muted.small { font-size: 0.8rem; }
	.small { font-size: 0.75rem; }

	.agents-table { width: 100%; font-size: 0.8rem; border-collapse: collapse; }
	.agents-table th {
		text-align: left; padding: 0.4rem 0.75rem;
		color: var(--color-text-muted); border-bottom: 1px solid var(--color-border);
		font-weight: 500;
	}
	.agents-table td {
		padding: 0.4rem 0.75rem;
		border-bottom: 1px solid var(--color-border);
	}
	.agents-table td.mono, .agents-table td .mono {
		font-family: var(--font-mono); font-size: 0.78rem;
	}
	.agents-table a {
		color: var(--color-primary); text-decoration: none;
	}
	.agents-table a:hover { text-decoration: underline; }

	.status-msg { color: var(--color-text-muted); padding: 2rem; text-align: center; }
	.status-msg.error { color: var(--color-error); }

	.detail-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 1.5rem; }
	.detail-section { background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: 0.5rem; padding: 1.25rem; }
	.detail-section.full { grid-column: 1 / -1; }
	.detail-section h2 { font-size: 0.9rem; font-weight: 600; margin-bottom: 1rem; color: var(--color-text-muted); text-transform: uppercase; letter-spacing: 0.04em; }
	.detail-table { width: 100%; font-size: 0.85rem; }
	.detail-table th { text-align: left; color: var(--color-text-muted); padding: 0.4rem 1rem 0.4rem 0; white-space: nowrap; width: 1%; }
	.detail-table td { padding: 0.4rem 0; }
	.detail-table .mono { font-family: var(--font-mono); font-size: 0.8rem; }
	.detail-table .uid { font-size: 0.7rem; color: var(--color-text-muted); }

	.conditions-table { width: 100%; font-size: 0.8rem; border-collapse: collapse; }
	.conditions-table th { text-align: left; padding: 0.4rem 0.75rem; color: var(--color-text-muted); border-bottom: 1px solid var(--color-border); }
	.conditions-table td { padding: 0.4rem 0.75rem; border-bottom: 1px solid var(--color-border); }
	.cond-true { color: var(--color-success); }
	.cond-false { color: var(--color-error); }
</style>
