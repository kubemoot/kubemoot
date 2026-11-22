<script lang="ts">
	import InfoPopover from '#lib/components/common/InfoPopover.svelte';
	import { SEARCH_HELP } from '#lib/search-query.js';

	interface Props {
		/** The query as the URL holds it. */
		value: string;
		onchange: (query: string) => void;
		placeholder?: string;
		label?: string;
	}

	let {
		value,
		onchange,
		placeholder = 'Search questions and messages',
		label = 'Search questions and messages'
	}: Props = $props();

	let timer: ReturnType<typeof setTimeout> | undefined;

	function typing(event: Event) {
		const text = (event.currentTarget as HTMLInputElement).value;
		clearTimeout(timer);
		timer = setTimeout(() => onchange(text.trim()), 250);
	}
</script>

<div class="field">
	<input class="search" type="search" {value} oninput={typing} {placeholder} aria-label={label} />
	<InfoPopover label="About search" lines={[SEARCH_HELP]} />
</div>

<style>
	.field { display: flex; align-items: center; gap: 0.4rem; }
	.search {
		flex: 1; min-width: 0; padding: 0.3rem 0.5rem; border-radius: 0.3rem; font-size: 0.8rem;
		border: 1px solid var(--color-border); background: var(--color-bg-secondary); color: var(--color-text);
	}
</style>
