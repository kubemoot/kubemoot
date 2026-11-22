<script lang="ts">
	import type { Snippet } from 'svelte';
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import SearchField from './SearchField.svelte';
	import { STATUSES, chipsOf, withoutChip, type ListFilter, type StatusFilter } from '#lib/conversation-filter.js';

	interface Props {
		filter: ListFilter;
		/** Mine needs a sign-in: the crew's gateway says which conversations are the person's. */
		signedIn: boolean;
		/** How many discussions are loaded for the search to look in. */
		loaded: number;
		/** Older discussions can still be loaded from the bus. */
		hasOlder: boolean;
		onchange: (filter: ListFilter) => void;
		onloadolder: () => void;
		/** A control beside the search field, such as + New. */
		trailing?: Snippet;
		/** Why "Mine" could not be worked out, in words. */
		error?: string;
	}

	let { filter, signedIn, loaded, hasOlder, onchange, onloadolder, trailing, error = '' }: Props = $props();

	const chips = $derived(chipsOf(filter));

	const pick = (event: Event) => {
		const value = (event.currentTarget as HTMLSelectElement).value;
		onchange({ ...filter, status: STATUSES.find((s): s is StatusFilter => s === value) ?? null });
	};
	let datesOpen = $state(false);
	let datesRoot = $state<HTMLElement | null>(null);

	/** What the date button says: Dates, or the range that is set. */
	function datesLabel(f: ListFilter): string {
		if (!f.from && !f.to) return 'Any date';
		return `${f.from ?? '...'} to ${f.to ?? '...'}`;
	}

	const date = (key: 'from' | 'to') => (event: Event) =>
		onchange({ ...filter, [key]: (event.currentTarget as HTMLInputElement).value || null });
</script>

<svelte:window onclick={(e) => { if (datesOpen && datesRoot && !datesRoot.contains(e.target as Node)) datesOpen = false; }} />

<div class="bar">
	<div class="search-row">
		<div class="grow"><SearchField value={filter.q} onchange={(q) => onchange({ ...filter, q })} /></div>
		{#if trailing}{@render trailing()}{/if}
	</div>
	<div class="row">
		<select value={filter.status ?? ''} onchange={pick} aria-label="Status" use:tooltip={ACTIONS.filterStatus}>
			<option value="">Any status</option>
			{#each STATUSES as s (s)}<option value={s}>{s}</option>{/each}
		</select>
		{#if signedIn}
			<label class="mine"><input use:tooltip={ACTIONS.filterMine} type="checkbox" checked={filter.mine} onchange={(e) => onchange({ ...filter, mine: e.currentTarget.checked })} /> Mine</label>
		{/if}
		<div class="dates" bind:this={datesRoot}>
			<button type="button" class="dates-btn" class:set={!!filter.from || !!filter.to} aria-expanded={datesOpen} use:tooltip={ACTIONS.filterDates} onclick={() => (datesOpen = !datesOpen)}>{datesLabel(filter)}</button>
			{#if datesOpen}
				<div class="dates-panel">
					<label>From <input type="date" value={filter.from ?? ''} onchange={date('from')} use:tooltip={ACTIONS.filterFrom} /></label>
					<label>To <input type="date" value={filter.to ?? ''} onchange={date('to')} use:tooltip={ACTIONS.filterTo} /></label>
				</div>
			{/if}
		</div>
	</div>
	{#if error}<p class="limit" role="alert">{error}</p>{/if}
	{#if chips.length > 0}
		<div class="chips">
			{#each chips as chip (chip.key)}
				<button type="button" class="chip" use:tooltip={ACTIONS.removeFilter} onclick={() => onchange(withoutChip(filter, chip.key))} aria-label="Remove filter {chip.label}">
					{chip.label} &times;
				</button>
			{/each}
		</div>
	{/if}
	{#if filter.q}
		<p class="limit">
			Searches the {loaded} discussions loaded from the bus.
			{#if hasOlder}<button type="button" class="link" use:tooltip={ACTIONS.loadOlderMatches} onclick={onloadolder}>Load older</button>{/if}
		</p>
	{/if}
</div>

<style>
	.bar { display: flex; flex-direction: column; gap: 0.35rem; margin-bottom: 0.4rem; }
	select, input[type='date'] {
		padding: 0.25rem 0.4rem; border-radius: 0.3rem; border: 1px solid var(--color-border);
		background: var(--color-bg-secondary); color: var(--color-text); font-size: 0.75rem; min-width: 0;
	}
	.search-row { display: flex; align-items: center; gap: 0.4rem; }
	.grow { flex: 1; min-width: 0; }
	.row { display: flex; flex-wrap: wrap; gap: 0.3rem; align-items: center; }
	.dates { position: relative; }
	.dates-btn {
		padding: 0.25rem 0.5rem; border-radius: 0.3rem; border: 1px solid var(--color-border); background: var(--color-bg-secondary);
		color: var(--color-text-muted); font-size: 0.75rem; cursor: pointer; white-space: nowrap;
	}
	.dates-btn.set { color: var(--color-text); border-color: var(--color-primary); }
	.dates-panel {
		position: absolute; left: 0; top: calc(100% + 0.25rem); z-index: 10; display: flex; flex-direction: column; gap: 0.4rem;
		padding: 0.5rem 0.6rem; background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: 0.375rem;
		box-shadow: 0 4px 12px rgba(0, 0, 0, 0.35); font-size: 0.75rem; color: var(--color-text-muted);
	}
	.dates-panel label { display: flex; align-items: center; justify-content: space-between; gap: 0.5rem; }
	.mine { display: flex; align-items: center; gap: 0.2rem; font-size: 0.75rem; color: var(--color-text-muted); }
	.chips { display: flex; flex-wrap: wrap; gap: 0.25rem; }
	.chip {
		background: var(--color-bg-tertiary); color: var(--color-text); border: 1px solid var(--color-border);
		border-radius: 999px; padding: 0.05rem 0.5rem; font-size: 0.7rem;
	}
	.limit { font-size: 0.68rem; color: var(--color-text-muted); }
	.link { background: none; border: none; color: var(--color-primary); font-size: 0.68rem; padding: 0; text-decoration: underline; }
</style>
