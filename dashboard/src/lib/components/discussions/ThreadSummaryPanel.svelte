<script lang="ts">
	import { formatCount, formatTimestamp, type ThreadSummary } from '#lib/discussion-timing.js';
	import { formatDuration } from '#lib/duration-format.js';

	interface Props {
		summary: ThreadSummary;
		/** Shows an agent's name as the timeline does. */
		agentLabel?: (agent: string) => string;
	}

	let { summary, agentLabel = (agent: string) => agent }: Props = $props();

	const SIGNAL_LABELS: Record<string, string> = {
		agree: 'agree',
		concern: 'concern',
		block: 'block',
		failure: 'failure',
		stand_aside: 'stood aside'
	};

	const names = (agents: string[]) => agents.map((a) => agentLabel(a)).join(', ');
	const tokens = (input?: number, output?: number) =>
		[input === undefined ? '' : `${formatCount(input)} in`, output === undefined ? '' : `${formatCount(output)} out`]
			.filter(Boolean)
			.join(' / ');

	const endLabel = $derived(summary.endedBy === 'close' ? 'Closed' : 'Answered');
	const agentTokens = $derived(tokens(summary.inputTokens, summary.outputTokens));
	const coordinatorTokens = $derived(tokens(summary.coordinatorInputTokens, summary.coordinatorOutputTokens));
	const signals = $derived(
		summary.signals.map((s) => `${s.count} ${SIGNAL_LABELS[s.kind] ?? s.kind}`).join(', ')
	);
	const rows = $derived(
		[
			{ label: 'Total time', value: formatDuration(summary.totalMs) },
			{ label: 'First signal', value: summary.firstSignalMs === undefined ? '' : `after ${formatDuration(summary.firstSignalMs)}` },
			{ label: 'Convened', value: summary.convened ? names(summary.convened) : '' },
			{ label: summary.convened ? 'Took part' : 'Agents', value: names(summary.participants) },
			{ label: 'Stood aside', value: names(summary.stoodAside) },
			{ label: 'Signals', value: signals },
			{ label: 'Agent tokens', value: agentTokens },
			{ label: 'Coordinator tokens', value: coordinatorTokens },
			{ label: 'Models', value: summary.models.join(', ') },
			{ label: 'Accelerators', value: summary.accelerators.join(', ') }
		].filter((r) => r.value !== '')
	);
</script>

<section class="thread-summary" aria-label="How this answer was made">
	{#if summary.endedAt}
		<div class="ended" title={summary.endedAt}>{endLabel} {formatTimestamp(summary.endedAt)}</div>
	{/if}
	{#if rows.length > 0}
		<h3>How this answer was made</h3>
		<dl>
			{#each rows as row (row.label)}
				<dt>{row.label}</dt>
				<dd>{row.value}</dd>
			{/each}
		</dl>
	{/if}
</section>

<style>
	.thread-summary {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		padding: 0.625rem 0.75rem;
		border: 1px dashed var(--color-border);
		border-radius: 0.5rem;
	}

	.ended {
		font-weight: 600;
		color: var(--color-text);
	}

	h3 {
		margin: 0.5rem 0 0.375rem;
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text);
	}

	dl {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 0.125rem 0.75rem;
		margin: 0;
	}

	dt {
		font-weight: 500;
	}

	dd {
		margin: 0;
		color: var(--color-text);
		font-family: var(--font-mono, monospace);
		overflow-wrap: anywhere;
	}
</style>
