<script lang="ts">
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import type { CrewScope } from '#lib/crewScope.js';
	import { exportFileName, exportQuery, exportSummary, fetchExportReport, type MemoryExportReport } from '#lib/crew-memory-export.js';

	interface Props {
		scope: CrewScope;
		/** The dashboard's export route. */
		endpoint: string;
		/** Whether the crew holds any facts; with none there is nothing to export. */
		hasFacts: boolean;
		/** Replaceable so a test can answer without a network. */
		fetchFn?: typeof fetch;
	}

	let { scope, endpoint, hasFacts, fetchFn = fetch }: Props = $props();

	let report = $state<MemoryExportReport | null>(null);

	// The screening report is only asked for when there is something to export.
	$effect(() => {
		const current = scope;
		report = null;
		if (!hasFacts) return;
		fetchExportReport(fetchFn, endpoint, current)
			.then((r) => {
				if (current === scope) report = r;
			})
			.catch(() => {
				// The download still works; only the note about left-out entries is missing.
			});
	});

	const leftOutNote = $derived(report && report.leftOut.length > 0 ? exportSummary(report) : null);
</script>

<div class="export">
	{#if leftOutNote}<span class="note">{leftOutNote}</span>{/if}
	{#if hasFacts}
		<a class="button" href={`${endpoint}?${exportQuery(scope, { report: false })}`} download={exportFileName(scope)} use:tooltip={ACTIONS.exportMemory}>Export</a>
	{:else}
		<!-- aria-disabled, not disabled: a disabled button gets no hover or focus, so its reason could not show. -->
		<button type="button" class="disabled" aria-disabled="true" use:tooltip={ACTIONS.exportMemoryEmpty} onclick={(e) => e.preventDefault()}>Export</button>
	{/if}
</div>

<style>
	.export { display: flex; align-items: center; gap: 0.75rem; }
	.note { color: var(--color-text-muted); font-size: 0.8rem; }
	a.button, button {
		display: inline-block; padding: 0.3rem 0.7rem; cursor: pointer; font-size: 0.8rem; text-decoration: none;
		background: var(--color-bg-tertiary); color: var(--color-text);
		border: 1px solid var(--color-border); border-radius: 0.375rem;
	}
	button.disabled { opacity: 0.5; cursor: not-allowed; }
</style>
