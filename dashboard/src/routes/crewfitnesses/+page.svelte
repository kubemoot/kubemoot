<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { crewDirectory, namespace, refreshTrigger } from '$stores';
	import { crewLabel, crewTooltip } from '$lib/crew-display-name';
	import type { CrewFitness } from '$types/kubemoot.js';

	let tests = $state<CrewFitness[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);

	async function fetchTests() {
		loading = true;
		error = null;
		try {
			const res = await fetch(`${resolve('/api/kubemoot/crewfitnesses')}?namespace=${$namespace}`);
			const data = await res.json();
			tests = (data.items || []).sort((a: CrewFitness, b: CrewFitness) =>
				(b.metadata.creationTimestamp ?? '').localeCompare(a.metadata.creationTimestamp ?? '')
			);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch fitness tests';
		} finally {
			loading = false;
		}
	}

	onMount(fetchTests);

	$effect(() => {
		$namespace;
		$refreshTrigger;
		fetchTests();
	});

	function phaseClass(phase?: string): string {
		switch (phase) {
			case 'Passed': return 'phase-pass';
			case 'Failed': case 'Error': return 'phase-fail';
			case 'Running': return 'phase-running';
			default: return 'phase-pending';
		}
	}

	function formatDuration(ms?: number): string {
		if (!ms) return '-';
		if (ms < 1000) return `${ms}ms`;
		return `${(ms / 1000).toFixed(1)}s`;
	}
</script>

<div class="page-header">
	<h1>Fitness Tests</h1>
	<span class="count">{tests.length}</span>
</div>

{#if loading}
	<p class="status-msg">Loading fitness tests...</p>
{:else if error}
	<p class="status-msg error">{error}</p>
{:else if tests.length === 0}
	<p class="status-msg">No CrewFitness CRs found in namespace <code>{$namespace}</code></p>
{:else}
	<table class="fitness-table">
		<thead>
			<tr>
				<th>Phase</th>
				<th>Name</th>
				<th>Crew</th>
				<th>Test</th>
				<th>Assertions</th>
				<th>Duration</th>
				<th>Age</th>
			</tr>
		</thead>
		<tbody>
			{#each tests as test}
				{@const s = test.status}
				{@const passed = s?.assertions?.filter(a => a.passed).length ?? 0}
				{@const total = s?.assertions?.length ?? 0}
				<tr>
					<td>
						<span class="phase-badge {phaseClass(s?.phase)}">
							{s?.phase ?? 'Unknown'}
						</span>
					</td>
					<td>
						<a href="{resolve('/crewfitnesses/[name]', { name: test.metadata.name })}?namespace={test.metadata.namespace}" class="name-link">
							{test.metadata.name}
						</a>
					</td>
					<td>
						<a href="{resolve('/crews/[name]', { name: test.spec.crewRef })}?namespace={test.metadata.namespace}" class="crew-link" title={crewTooltip($crewDirectory, test.metadata.namespace, test.spec.crewRef)}>
							{crewLabel($crewDirectory, test.metadata.namespace, test.spec.crewRef)}
						</a>
					</td>
					<td class="mono">{test.spec.testRef}</td>
					<td>
						{#if total > 0}
							<span class:all-pass={passed === total} class:has-fail={passed < total}>
								{passed}/{total}
							</span>
						{:else}
							-
						{/if}
					</td>
					<td class="mono">{formatDuration(s?.durationMs)}</td>
					<td class="muted">{test.metadata.creationTimestamp ? new Date(test.metadata.creationTimestamp).toLocaleString() : '-'}</td>
				</tr>
			{/each}
		</tbody>
	</table>
{/if}

<style>
	.page-header {
		display: flex; align-items: center; gap: 0.75rem; margin-bottom: 1.5rem;
	}
	.page-header h1 { font-size: 1.5rem; font-weight: 600; }
	.count {
		background: var(--color-bg-tertiary); padding: 0.15rem 0.6rem;
		border-radius: 999px; font-size: 0.8rem; color: var(--color-text-muted);
	}

	.status-msg { color: var(--color-text-muted); padding: 2rem; text-align: center; }
	.status-msg.error { color: var(--color-error); }
	.status-msg code { color: var(--color-cyan); font-family: var(--font-mono); }

	.fitness-table {
		width: 100%; border-collapse: collapse;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem; overflow: hidden;
	}
	.fitness-table th {
		text-align: left; padding: 0.75rem 1rem;
		font-size: 0.7rem; font-weight: 600;
		text-transform: uppercase; letter-spacing: 0.04em;
		color: var(--color-text-muted);
		border-bottom: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
	}
	.fitness-table td {
		padding: 0.6rem 1rem; font-size: 0.85rem;
		border-bottom: 1px solid var(--color-border);
	}
	.fitness-table tr:last-child td { border-bottom: none; }
	.fitness-table tr:hover td { background: var(--color-bg-tertiary); }

	.name-link { color: var(--color-primary); font-family: var(--font-mono); font-size: 0.8rem; }
	.crew-link { color: var(--color-cyan); font-family: var(--font-mono); font-size: 0.8rem; }
	.mono { font-family: var(--font-mono); font-size: 0.8rem; }
	.muted { color: var(--color-text-muted); font-size: 0.8rem; }

	.phase-badge {
		font-size: 0.7rem; font-weight: 600; padding: 0.15rem 0.5rem;
		border-radius: 999px; text-transform: uppercase; letter-spacing: 0.04em;
		display: inline-block;
	}
	.phase-pass { background: rgba(34, 197, 94, 0.15); color: var(--color-success); }
	.phase-fail { background: rgba(239, 68, 68, 0.15); color: var(--color-error); }
	.phase-running { background: rgba(59, 130, 246, 0.15); color: var(--color-primary); }
	.phase-pending { background: var(--color-bg-tertiary); color: var(--color-text-muted); }

	.all-pass { color: var(--color-success); font-weight: 600; }
	.has-fail { color: var(--color-error); font-weight: 600; }
</style>
