<script lang="ts">
	import type { ErasureReport } from '#lib/conversation-actions-client.js';
	import { removedLines } from '#lib/conversation-actions.js';

	interface Props {
		report: ErasureReport;
		/** The conversation's first question, so the person sees which one it was. */
		question: string;
		ondismiss: () => void;
	}

	let { report, question, ondismiss }: Props = $props();

	const removed = $derived(removedLines(report));
</script>

<section class="report" aria-label="Deleted conversation">
	<h2>Conversation deleted</h2>
	{#if question}<p class="question">{question}</p>{/if}
	{#if removed.length > 0}
		<p class="label">Removed</p>
		<ul>{#each removed as line (line)}<li>{line}</li>{/each}</ul>
	{:else}
		<p class="muted">Nothing was left in any store.</p>
	{/if}
	{#if report.kept.length > 0}
		<p class="label">Not removed</p>
		<ul class="muted">{#each report.kept as line (line)}<li>{line}</li>{/each}</ul>
	{/if}
	<button type="button" onclick={ondismiss}>Close</button>
</section>

<style>
	.report { margin: auto; max-width: 34rem; padding: 1rem 1.25rem; font-size: 0.85rem; border: 1px solid var(--color-border); border-radius: 0.5rem; background: var(--color-bg-secondary); }
	h2 { font-size: 1rem; margin-bottom: 0.4rem; }
	.question { color: var(--color-text-muted); margin-bottom: 0.5rem; overflow-wrap: anywhere; }
	.label { font-weight: 600; margin-top: 0.5rem; }
	ul { margin: 0.2rem 0 0; padding-left: 1.2rem; }
	.muted { color: var(--color-text-muted); }
	button { margin-top: 0.75rem; background: none; border: 1px solid var(--color-border); border-radius: 0.3rem; padding: 0.15rem 0.7rem; color: var(--color-text); cursor: pointer; }
</style>
