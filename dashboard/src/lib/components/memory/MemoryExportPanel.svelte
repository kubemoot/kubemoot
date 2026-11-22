<script lang="ts">
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import type { CrewScope } from '#lib/crewScope.js';
	import {
		exportFileName,
		exportQuery,
		exportSummary,
		fetchExportReport,
		type MemoryExportReport
	} from '#lib/crew-memory-export.js';

	interface Props {
		scope: CrewScope;
		/** The dashboard's export route. */
		endpoint: string;
		/** Replaceable so a test can answer without a network. */
		fetchFn?: typeof fetch;
	}

	let { scope, endpoint, fetchFn = fetch }: Props = $props();

	let report = $state<MemoryExportReport | null>(null);
	let problem = $state<string | null>(null);

	async function load(current: CrewScope) {
		report = null;
		problem = null;
		try {
			const result = await fetchExportReport(fetchFn, endpoint, current);
			// An answer for a crew that is no longer selected is dropped.
			if (current === scope) report = result;
		} catch (e) {
			problem = e instanceof Error ? e.message : 'Failed to prepare the export';
		}
	}

	$effect(() => {
		load(scope);
	});
</script>

<div class="card export" aria-label="Export crew memory">
	<p>
		Downloads a zip of this crew's memory in the OMPI (Open Memory Protocol Initiative) interchange format, working spec
		v0.6: a manifest and one memory object per fact. The operator builds it.
	</p>
	<p class="hint">
		Screened: a fact whose topic, key or value holds a credential is left out. For an export that keeps them, use
		<code>kmctl</code>, which runs with your own credentials.
	</p>
	{#if problem}
		<p class="error" role="alert">{problem}</p>
	{:else if !report}
		<p class="hint">Preparing the export...</p>
	{:else}
		<p>{exportSummary(report)}</p>
		{#if report.leftOut.length > 0}
			<table aria-label="Entries left out of the export">
				<thead><tr><th>Entry</th><th>Why it was left out</th></tr></thead>
				<tbody>
					{#each report.leftOut as entry (entry.key)}
						<tr><td class="val">{entry.key}</td><td class="val">{entry.reason}</td></tr>
					{/each}
				</tbody>
			</table>
		{/if}
		<a use:tooltip={ACTIONS.downloadMemory}
			class="button primary"
			href={`${endpoint}?${exportQuery(scope, { report: false })}`}
			download={exportFileName(scope)}>Download</a
		>
	{/if}
</div>

<style>
	.export { padding: 0.75rem 1rem; margin: 0.75rem 0; color: var(--color-text); background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: 0.5rem; }
	table { width: 100%; border-collapse: collapse; font-size: 0.9rem; }
	th, td { text-align: left; padding: 0.4rem 0.6rem; border-bottom: 1px solid var(--color-border); vertical-align: top; }
	th { color: var(--color-text-muted); font-weight: 600; font-size: 0.8rem; }
	.val { word-break: break-word; }
	.hint { color: var(--color-text-muted); font-size: 0.85rem; }
	.error { color: var(--color-error); }
	a.button {
		display: inline-block; margin-top: 0.5rem; padding: 0.3rem 0.7rem; font-size: 0.8rem; text-decoration: none;
		color: var(--color-text); border: 1px solid var(--color-border); border-radius: 0.375rem;
	}
	a.button.primary { background: var(--color-primary); border-color: var(--color-primary); }
</style>
