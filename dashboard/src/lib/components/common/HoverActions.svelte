<script lang="ts">
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import { showsTick, type HoverAction } from '#lib/hover-actions.js';

	interface Props {
		actions: HoverAction[];
		/** Names the bar for assistive technology, such as "Message actions". */
		label: string;
	}

	// The bar shows over its parent while the pointer is over it or focus is inside it. Put the class
	// "has-hover-actions" on the parent (and position it); the bar stays reachable by keyboard while hidden.
	let { actions, label }: Props = $props();

	let ticked = $state<string | null>(null);
	let timer: ReturnType<typeof setTimeout> | undefined;

	async function perform(action: HoverAction) {
		try {
			await action.run();
		} catch {
			return;
		}
		if (!showsTick(action.icon)) return;
		ticked = action.label;
		clearTimeout(timer);
		timer = setTimeout(() => (ticked = null), 1500);
	}
</script>

<div class="hover-actions" role="toolbar" aria-label={label}>
	{#each actions as action (action.label)}
		<button type="button" class="icon" class:danger={action.danger} aria-label={action.label} use:tooltip={ACTIONS.hoverTip(action.id)} onclick={(e) => { e.stopPropagation(); void perform(action); }}>
			<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
				{#if ticked === action.label}
					<polyline points="20 6 9 17 4 12" />
				{:else if action.icon === 'copy'}
					<rect x="9" y="9" width="13" height="13" rx="2" /><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
				{:else if action.icon === 'id'}
					<line x1="4" y1="9" x2="20" y2="9" /><line x1="4" y1="15" x2="20" y2="15" /><line x1="10" y1="3" x2="8" y2="21" /><line x1="16" y1="3" x2="14" y2="21" />
				{:else if action.icon === 'edit'}
					<path d="M12 20h9" /><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
				{:else}
					<polyline points="3 6 5 6 21 6" /><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6" /><path d="M10 11v6M14 11v6" /><path d="M9 6V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2" />
				{/if}
			</svg>
		</button>
	{/each}
	<span class="sr-only" role="status">{ticked ? `${ticked}: done` : ''}</span>
</div>

<style>
	.hover-actions {
		position: absolute; top: var(--hover-actions-top, 0.3rem); right: var(--hover-actions-right, 0.4rem); z-index: 2; display: flex; gap: 0.1rem; padding: 0.1rem;
		background: var(--color-bg); border: 1px solid var(--color-border); border-radius: 0.375rem;
		opacity: 0; pointer-events: none; transition: opacity 0.1s;
	}
	:global(.has-hover-actions:hover) > .hover-actions,
	:global(.has-hover-actions:focus-within) > .hover-actions {
		opacity: 1; pointer-events: auto;
	}
	/* No hover on a touch screen: the bar is always there. */
	@media (hover: none) {
		.hover-actions { opacity: 1; pointer-events: auto; }
	}
	.icon {
		display: inline-flex; align-items: center; justify-content: center; width: 1.5rem; height: 1.5rem; padding: 0;
		background: none; border: none; border-radius: 0.25rem; color: var(--color-text-muted); cursor: pointer;
	}
	.icon:hover { color: var(--color-text); background: var(--color-bg-secondary); }
	.icon:focus-visible { outline: 2px solid var(--color-primary); outline-offset: 1px; color: var(--color-text); }
	.icon.danger:hover { color: var(--color-error); }
	.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
</style>
