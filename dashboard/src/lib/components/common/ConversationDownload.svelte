<script lang="ts">
	import type { CrewScope } from '#lib/crewScope.js';
	import { browserTimeZone, exportHref, exportLabel } from '#lib/conversation-export.js';

	interface Props {
		scope: CrewScope | null;
		/** The conversations to download: one, or several for a zip of zips. */
		ids: readonly string[];
		/** The dashboard's export route. */
		endpoint: string;
		/** Text for the button; the default says how many. */
		label?: string;
		/** Replaceable so a test can fix the zone. */
		timeZone?: string;
	}

	let { scope, ids, endpoint, label, timeZone = browserTimeZone() }: Props = $props();

	const href = $derived(exportHref(endpoint, scope, ids, timeZone));
</script>

{#if href}
	<a
		class="action-btn"
		{href}
		download
		>{label ?? exportLabel(ids.length)}</a
	>
{/if}

<style>
	.action-btn {
		background: none; border: 1px solid var(--color-border); border-radius: 0.3rem;
		padding: 0.15rem 0.5rem; cursor: pointer; color: var(--color-text-muted); font-size: 0.8rem; text-decoration: none;
	}
	.action-btn:hover { border-color: var(--color-primary); text-decoration: none; }
</style>
