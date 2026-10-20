<script lang="ts">
	import { formatCount, type MessageStats } from '#lib/discussion-timing.js';
	import { formatDuration } from '#lib/duration-format.js';

	interface Props {
		stats: MessageStats;
		/** Server id to the hardware it runs on, shown on hover beside the server name. */
		acceleratorNames?: Record<string, string>;
	}

	let { stats, acceleratorNames = {} }: Props = $props();

	const steps = $derived(
		[
			{ label: 'triage', ms: stats.triageMs, hint: "From the agent's triaging to its evaluating, or to its signal" },
			{ label: 'evaluation', ms: stats.evaluationMs, hint: "From the agent's evaluating to its signal" },
			{ label: 'inference', ms: stats.inferenceMs, hint: 'Time spent in model calls' }
		].filter((s) => s.ms !== undefined)
	);
	const hasTokens = $derived(stats.inputTokens !== undefined || stats.outputTokens !== undefined);
	const hardware = $derived(stats.accelerator ? acceleratorNames[stats.accelerator] : undefined);
	const visible = $derived(steps.length > 0 || hasTokens || !!stats.model || !!stats.accelerator);
</script>

{#if visible}
	<div class="stats-line">
		{#each steps as step (step.label)}
			<span class="stat" title={step.hint}>{step.label} <strong>{formatDuration(step.ms)}</strong></span>
		{/each}
		{#if hasTokens}
			<span class="stat" title="Tokens in and out of the model calls behind this message">
				{#if stats.inputTokens !== undefined}<strong>{formatCount(stats.inputTokens)}</strong> in{/if}
				{#if stats.inputTokens !== undefined && stats.outputTokens !== undefined}/{/if}
				{#if stats.outputTokens !== undefined}<strong>{formatCount(stats.outputTokens)}</strong> out{/if}
				tokens
			</span>
		{/if}
		{#if stats.model}
			<span class="stat model" title="Model">{stats.model}</span>
		{/if}
		{#if stats.accelerator}
			<span class="stat accelerator" title={hardware ? `Accelerator: ${hardware}` : 'Accelerator'}>on {stats.accelerator}</span>
		{/if}
	</div>
{/if}

<style>
	.stats-line {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
		margin-top: 0.375rem;
		font-size: 0.65rem;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-muted);
	}

	.stat {
		padding: 0.0625rem 0.375rem;
		border-radius: 0.25rem;
		background: rgba(107, 114, 128, 0.12);
	}

	.stat strong {
		font-weight: 600;
		color: var(--color-text);
	}

	.model {
		background: rgba(14, 165, 233, 0.12);
	}

	.accelerator {
		background: rgba(168, 85, 247, 0.12);
	}
</style>
