<script lang="ts">
	import type { Snippet } from 'svelte';
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import SearchField from './SearchField.svelte';
	import { STATUSES, chipsOf, withoutChip, type ListFilter, type StatusFilter } from '#lib/conversation-filter.js';
	import { RANGES, customLabel, isRangeKey, rangeLabel } from '#lib/time-range.js';

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

	let customOpen = $state(false);
	let customRoot = $state<HTMLElement | null>(null);
	let panelTop = $state(0);
	let panelLeft = $state(0);

	const PANEL_WIDTH_PX = 300;

	/** The panel is fixed to the window, so the list column's scrolling edge cannot cut it off. */
	$effect(() => {
		if (!customOpen || !customRoot) return;
		const rect = customRoot.getBoundingClientRect();
		panelTop = rect.bottom + 4;
		panelLeft = Math.max(8, Math.min(rect.left, window.innerWidth - PANEL_WIDTH_PX - 8));
	});

	/** Choosing a range sets it; Custom keeps the days already chosen and opens the From and To inputs. */
	const pickRange = (event: Event) => {
		const value = (event.currentTarget as HTMLSelectElement).value;
		if (!isRangeKey(value)) return;
		if (value === 'custom') {
			customOpen = true;
			onchange({ ...filter, range: 'custom' });
			return;
		}
		customOpen = false;
		onchange({ ...filter, range: value, from: null, to: null });
	};

	const setEnd = (key: 'from' | 'to') => (event: Event) =>
		onchange({ ...filter, range: 'custom', [key]: (event.currentTarget as HTMLInputElement).value || null });

	const closeOnOutside = (event: MouseEvent) => {
		if (customOpen && customRoot && !customRoot.contains(event.target as Node)) customOpen = false;
	};
	const closeOnEscape = (event: KeyboardEvent) => {
		if (!customOpen || event.key !== 'Escape') return;
		customOpen = false;
		customRoot?.querySelector<HTMLElement>('.custom-btn, select')?.focus();
	};
</script>

<svelte:window onclick={closeOnOutside} onkeydown={closeOnEscape} />

<div class="bar">
	<div class="search-row">
		<div class="grow"><SearchField value={filter.q} onchange={(q) => onchange({ ...filter, q })} /></div>
		{#if trailing}{@render trailing()}{/if}
	</div>
	<div class="row">
		<select value={filter.status ?? ''} onchange={pick} aria-label="Status">
			<option value="">Any status</option>
			{#each STATUSES as s (s)}<option value={s}>{s}</option>{/each}
		</select>
		{#if signedIn}
			<label class="mine" use:tooltip={ACTIONS.filterMine}><input type="checkbox" checked={filter.mine} onchange={(e) => onchange({ ...filter, mine: e.currentTarget.checked })} /> Mine</label>
		{/if}
		<div class="range" bind:this={customRoot}>
			<select value={filter.range} onchange={pickRange} aria-label="Time range">
				{#each RANGES as r (r)}<option value={r}>{rangeLabel(r)}</option>{/each}
			</select>
			{#if filter.range === 'custom'}
				<button type="button" class="custom-btn" aria-expanded={customOpen} onclick={() => (customOpen = !customOpen)}>{filter.from || filter.to ? customLabel(filter.from, filter.to) : 'Choose dates'}</button>
			{/if}
			{#if customOpen}
				<div class="custom-panel" role="group" aria-label="Custom time range" style:top="{panelTop}px" style:left="{panelLeft}px">
					<label>From <input type="datetime-local" value={filter.from ?? ''} onchange={setEnd('from')} /></label>
					<label>To <input type="datetime-local" value={filter.to ?? ''} onchange={setEnd('to')} /></label>
				</div>
			{/if}
		</div>
	</div>
	{#if error}<p class="limit" role="alert">{error}</p>{/if}
	{#if chips.length > 0}
		<div class="chips">
			{#each chips as chip (chip.key)}
				<button type="button" class="chip" onclick={() => onchange(withoutChip(filter, chip.key))} aria-label="Remove filter {chip.label}">
					{chip.label} &times;
				</button>
			{/each}
		</div>
	{/if}
	{#if filter.q}
		<p class="limit">
			Searches the {loaded} discussions loaded from the bus.
			{#if hasOlder}<button type="button" class="link" onclick={onloadolder}>Load older</button>{/if}
		</p>
	{/if}
</div>

<style>
	.bar { display: flex; flex-direction: column; gap: 0.35rem; margin-bottom: 0.4rem; }
	select, input[type='datetime-local'] {
		padding: 0.25rem 0.4rem; border-radius: 0.3rem; border: 1px solid var(--color-border);
		background: var(--color-bg-secondary); color: var(--color-text); font-size: 0.75rem; min-width: 0;
	}
	.search-row { display: flex; align-items: center; gap: 0.4rem; }
	.grow { flex: 1; min-width: 0; }
	.row { display: flex; flex-wrap: wrap; gap: 0.3rem; align-items: center; }
	.range { position: relative; display: flex; gap: 0.3rem; align-items: center; }
	.custom-btn {
		padding: 0.25rem 0.5rem; border-radius: 0.3rem; border: 1px solid var(--color-primary); background: var(--color-bg-secondary);
		color: var(--color-text); font-size: 0.75rem; white-space: nowrap;
	}
	.custom-panel {
		position: fixed; z-index: 10; width: 300px; display: flex; flex-direction: column; gap: 0.4rem;
		padding: 0.5rem 0.6rem; background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: 0.375rem;
		box-shadow: var(--shadow-popup); font-size: 0.75rem; color: var(--color-text-muted);
	}
	.custom-panel label { display: flex; align-items: center; justify-content: space-between; gap: 0.5rem; }
	.custom-panel input { flex: 1; }
	.mine { display: flex; align-items: center; gap: 0.2rem; font-size: 0.75rem; color: var(--color-text-muted); }
	.chips { display: flex; flex-wrap: wrap; gap: 0.25rem; }
	.chip {
		background: var(--color-bg-tertiary); color: var(--color-text); border: 1px solid var(--color-border);
		border-radius: 999px; padding: 0.05rem 0.5rem; font-size: 0.7rem;
	}
	.limit { font-size: 0.68rem; color: var(--color-text-muted); }
	.link { background: none; border: none; color: var(--color-primary); font-size: 0.68rem; padding: 0; text-decoration: underline; }
</style>
