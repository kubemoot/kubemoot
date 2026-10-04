<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import type { CrewFitness } from '#lib/types/kubemoot.js';
	import { crewDirectory } from '#lib/stores/index.js';
	import { crewLabel, crewTooltip } from '#lib/crew-display-name.js';

	let test = $state<CrewFitness | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);

	$effect(() => {
		const name = page.params.name as string;
		const ns = page.url.searchParams.get('namespace') || 'kubemoot';
		if (name) fetchTest(ns, name);
	});

	async function fetchTest(ns: string, name: string) {
		loading = true;
		error = null;
		try {
			const res = await fetch(`${resolve('/api/kubemoot/crewfitnesses/[name]', { name })}?namespace=${ns}`);
			const data = await res.json();
			if (data.error) throw new Error(data.error);
			test = data;
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch fitness test';
		} finally {
			loading = false;
		}
	}

	function formatDuration(ms?: number): string {
		if (!ms) return '-';
		if (ms < 1000) return `${ms}ms`;
		return `${(ms / 1000).toFixed(1)}s`;
	}
</script>

<div class="detail-header">
	<a href="{resolve('/crewfitnesses')}" class="back-link">&larr; Fitness Tests</a>
	{#if test}
		<h1>{test.metadata.name}</h1>
		{@const phase = test.status?.phase ?? 'Unknown'}
		<span class="phase-badge" class:pass={phase === 'Passed'} class:fail={phase === 'Failed' || phase === 'Error'} class:running={phase === 'Running'}>
			{phase}
		</span>
	{/if}
</div>

{#if loading}
	<p class="status-msg">Loading...</p>
{:else if error}
	<p class="status-msg error">{error}</p>
{:else if test}
	{@const s = test.status}
	<div class="detail-grid">
		<div class="detail-section">
			<h2>Spec</h2>
			<table class="detail-table"><tbody>
				<tr><th>Crew</th><td><a href="{resolve('/crews/[name]', { name: test.spec.crewRef })}?namespace={test.metadata.namespace}" class="link" title={crewTooltip($crewDirectory, test.metadata.namespace, test.spec.crewRef)}>{crewLabel($crewDirectory, test.metadata.namespace, test.spec.crewRef)}</a></td></tr>
				<tr><th>Test Ref</th><td class="mono">{test.spec.testRef}</td></tr>
				<tr><th>ConfigMap</th><td class="mono">{test.spec.configMapRef}</td></tr>
				{#if test.spec.ttl}
					<tr><th>TTL</th><td>{test.spec.ttl}</td></tr>
				{/if}
			</tbody></table>
		</div>

		<div class="detail-section">
			<h2>Status</h2>
			<table class="detail-table"><tbody>
				<tr><th>Phase</th><td>{s?.phase ?? '-'}</td></tr>
				<tr><th>Duration</th><td class="mono">{formatDuration(s?.durationMs)}</td></tr>
				{#if s?.jobRef}
					<tr><th>Job</th><td class="mono">{s.jobRef}</td></tr>
				{/if}
				{#if s?.startedAt}
					<tr><th>Started</th><td>{new Date(s.startedAt).toLocaleString()}</td></tr>
				{/if}
				{#if s?.completedAt}
					<tr><th>Completed</th><td>{new Date(s.completedAt).toLocaleString()}</td></tr>
				{/if}
				{#if s?.error}
					<tr><th>Error</th><td class="error-text">{s.error}</td></tr>
				{/if}
			</tbody></table>
		</div>
	</div>

	{#if s?.assertions && s.assertions.length > 0}
		<div class="detail-section full">
			<h2>Assertions ({s.assertions.filter(a => a.passed).length}/{s.assertions.length} passed)</h2>
			<table class="assertions-table">
				<thead><tr><th>Result</th><th>Assertion</th><th>Message</th></tr></thead>
				<tbody>
					{#each s.assertions as ar}
						<tr class:passed={ar.passed} class:failed={!ar.passed}>
							<td class="result-icon">{ar.passed ? '\u2713' : '\u2717'}</td>
							<td class="mono">{ar.raw}</td>
							<td>{ar.message}</td>
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
	.phase-badge {
		font-size: 0.75rem; font-weight: 600; padding: 0.2rem 0.6rem;
		border-radius: 999px; text-transform: uppercase;
		background: var(--color-bg-tertiary); color: var(--color-text-muted);
	}
	.phase-badge.pass { background: rgba(34, 197, 94, 0.15); color: var(--color-success); }
	.phase-badge.fail { background: rgba(239, 68, 68, 0.15); color: var(--color-error); }
	.phase-badge.running { background: rgba(59, 130, 246, 0.15); color: var(--color-primary); }

	.status-msg { color: var(--color-text-muted); padding: 2rem; text-align: center; }
	.status-msg.error { color: var(--color-error); }

	.detail-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 1.5rem; margin-bottom: 1.5rem; }
	.detail-section { background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: 0.5rem; padding: 1.25rem; }
	.detail-section.full { grid-column: 1 / -1; }
	.detail-section h2 { font-size: 0.9rem; font-weight: 600; margin-bottom: 1rem; color: var(--color-text-muted); text-transform: uppercase; letter-spacing: 0.04em; }
	.detail-table { width: 100%; font-size: 0.85rem; }
	.detail-table th { text-align: left; color: var(--color-text-muted); padding: 0.4rem 1rem 0.4rem 0; white-space: nowrap; width: 1%; }
	.detail-table td { padding: 0.4rem 0; }
	.mono { font-family: var(--font-mono); font-size: 0.8rem; }
	.link { color: var(--color-primary); }
	.error-text { color: var(--color-error); }

	.assertions-table { width: 100%; border-collapse: collapse; font-size: 0.85rem; }
	.assertions-table th { text-align: left; padding: 0.4rem 0.75rem; color: var(--color-text-muted); border-bottom: 1px solid var(--color-border); font-size: 0.75rem; text-transform: uppercase; }
	.assertions-table td { padding: 0.4rem 0.75rem; border-bottom: 1px solid var(--color-border); }
	.assertions-table .mono { font-size: 0.8rem; }
	.result-icon { width: 2rem; text-align: center; font-weight: 600; }
	.passed .result-icon { color: var(--color-success); }
	.failed .result-icon { color: var(--color-error); }
</style>
