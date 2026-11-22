<script lang="ts">
	import type { PlacementCall } from '#lib/types/bus.js';
	import {
		placementRow,
		placementSummaryLine,
		summarizePlacement
	} from '#lib/placement-panel.js';

	interface Props {
		calls: PlacementCall[];
		/** Shows an agent's name as the timeline does. */
		agentLabel?: (agent: string) => string;
	}

	let { calls, agentLabel = (agent: string) => agent }: Props = $props();

	const summary = $derived(placementSummaryLine(summarizePlacement(calls)));
	const rows = $derived(calls.map(placementRow));
</script>

<section class="placement" aria-label="Placement">
	<h3>Placement</h3>
	{#if rows.length === 0}
		<p class="note">No placement decisions were recorded for this thread.</p>
	{:else}
		<p class="summary">{summary}</p>
		<ul>
			{#each rows as row, i (i)}
				<li class="call">
					<div class="head">
						<span class="who">{row.agent ? agentLabel(row.agent) : 'unknown'}</span>
						{#if row.phase}<span class="phase">{row.phase}</span>{/if}
						<span class="place">{row.place}</span>
						<span class="badge policy-{row.policy}">{row.policy}</span>
						{#if row.coldStart}<span class="badge cold" title="The model had to be loaded">cold start</span>{/if}
						{#if row.failed}<span class="badge failed">failed</span>{/if}
					</div>
					{#if row.reason}<div class="reason">{row.reason}</div>{/if}
					<div class="numbers">
						{#if row.hasPrediction}
							<span>total: predicted {row.predictedTotal}{row.actualTotal ? `, actual ${row.actualTotal}` : ''}{row.totalError ? `, error ${row.totalError}` : ''}</span>
							{#if row.predictedFirstToken && row.actualFirstToken}
								<span>first token: predicted {row.predictedFirstToken}, actual {row.actualFirstToken}{row.firstTokenError ? `, error ${row.firstTokenError}` : ''}</span>
							{/if}
						{:else}
							<span>no prediction{row.actualTotal ? `; actual total ${row.actualTotal}` : ''}</span>
						{/if}
					</div>
					{#if row.alternatives.length > 0}
						<details>
							<summary>{row.alternatives.length} {row.alternatives.length === 1 ? 'alternative' : 'alternatives'} considered</summary>
							<ul class="alternatives">
								{#each row.alternatives as alt, j (j)}
									<li>
										<span class="place">{alt.place}</span>
										<span>predicted {alt.total}</span>
										{#if alt.parts}
											<span class="parts">wait {alt.parts.wait}, load {alt.parts.load}, prompt {alt.parts.prompt}, generate {alt.parts.generate}</span>
										{/if}
										<span class="parts">{alt.warm ? 'warm' : 'cold'}{alt.startsNow ? ', starts now' : ', waits for a slot'}</span>
									</li>
								{/each}
							</ul>
						</details>
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
</section>

<style>
	.placement {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		padding: 0.625rem 0.75rem;
		border: 1px dashed var(--color-border);
		border-radius: 0.5rem;
		margin-top: 0.75rem;
	}

	h3 {
		margin: 0 0 0.375rem;
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.summary,
	.note {
		margin: 0 0 0.5rem;
	}

	ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.call {
		padding-top: 0.5rem;
		border-top: 1px solid var(--color-border);
	}

	.head {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
		align-items: center;
	}

	.who {
		font-weight: 600;
		color: var(--color-text);
	}

	.place {
		font-family: var(--font-mono, monospace);
		color: var(--color-text);
	}

	.badge {
		padding: 0 0.375rem;
		border-radius: 0.25rem;
		border: 1px solid var(--color-border);
		font-size: 0.6875rem;
		color: var(--color-text);
	}

	.policy-learned {
		border-color: #3b82f6;
	}

	.policy-algorithm {
		border-color: #6b7280;
	}

	.policy-shadow {
		border-style: dashed;
	}

	.cold {
		border-color: #f59e0b;
	}

	.failed {
		border-color: #ef4444;
	}

	.reason {
		margin-top: 0.25rem;
		overflow-wrap: anywhere;
	}

	.numbers {
		margin-top: 0.25rem;
		display: flex;
		flex-direction: column;
		font-family: var(--font-mono, monospace);
		color: var(--color-text);
	}

	details {
		margin-top: 0.25rem;
	}

	summary {
		cursor: pointer;
	}

	.alternatives {
		margin-top: 0.25rem;
		gap: 0.25rem;
	}

	.alternatives li {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.parts {
		font-family: var(--font-mono, monospace);
	}
</style>
