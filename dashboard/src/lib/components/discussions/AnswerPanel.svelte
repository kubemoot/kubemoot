<script lang="ts">
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import { marked } from 'marked';
	import ThreadSummaryPanel from './ThreadSummaryPanel.svelte';
	import type { AnswerState } from '#lib/conversation-answer.js';
	import type { ThreadSummary } from '#lib/discussion-timing.js';

	interface Props {
		state: AnswerState;
		/** How long the answer took, already formatted; empty when unknown. */
		elapsed?: string;
		summary?: ThreadSummary | null;
		agentLabel?: (agent: string) => string;
	}

	let { state, elapsed = '', summary = null, agentLabel }: Props = $props();

	const hasSummary = $derived(!!summary && (!!summary.endedAt || summary.participants.length > 0));
</script>

<section class="answer" class:pending={state.kind !== 'answered'} aria-label="Answer">
	{#if state.kind === 'answered'}
		<div class="answer-head">
			<h2>Answer</h2>
			{#if elapsed}<span class="elapsed">{elapsed}</span>{/if}
		</div>
		<div class="answer-body markdown">{@html marked.parse(state.text)}</div>
		{#if state.note}<p class="sources-note" role="note">{state.note}</p>{/if}
		{#if hasSummary && summary}
			<details class="made">
				<summary use:tooltip={ACTIONS.howAnswerMade}>How this answer was made</summary>
				<ThreadSummaryPanel {summary} {agentLabel} />
			</details>
		{/if}
	{:else if state.kind === 'working'}
		<p class="status">No answer yet. The crew is still working on it.</p>
	{:else if state.kind === 'stopped'}
		<p class="status">No answer. This conversation was stopped.</p>
	{:else}
		<p class="status failed">No answer. It failed: {state.reason}</p>
	{/if}
</section>

<style>
	.answer {
		flex-shrink: 0;
		max-height: 45vh;
		overflow-y: auto;
		padding: 0.875rem 1rem;
		border: 1px solid var(--color-primary);
		border-left-width: 4px;
		border-radius: 0.5rem;
		background: var(--color-bg-secondary);
	}
	.answer.pending { border-color: var(--color-border); border-left-width: 1px; }
	.answer-head { display: flex; align-items: baseline; gap: 0.75rem; }
	h2 { margin: 0 0 0.375rem; font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.04em; color: var(--color-text-muted); }
	.elapsed { font-size: 0.75rem; color: var(--color-text-muted); font-family: var(--font-mono, monospace); }
	.answer-body { font-size: 1.05rem; line-height: 1.5; color: var(--color-text); overflow-wrap: anywhere; }
	.answer-body :global(p) { margin: 0 0 0.5rem; }
	.answer-body :global(ul), .answer-body :global(ol) { margin: 0 0 0.5rem; padding-left: 1.5rem; }
	.sources-note { margin: 0.5rem 0 0; font-size: 0.75rem; color: var(--color-text-muted); }
	.made { margin-top: 0.625rem; font-size: 0.75rem; color: var(--color-text-muted); }
	.made summary { cursor: pointer; }
	.status { margin: 0; color: var(--color-text-muted); }
	.status.failed { color: var(--color-text); overflow-wrap: anywhere; }
</style>
