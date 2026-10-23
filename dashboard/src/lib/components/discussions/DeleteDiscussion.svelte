<script lang="ts">
	import { DELETE_TOOLTIP } from '#lib/discussion-actions.js';

	interface Props {
		/** Called after the person answers Yes. */
		onconfirm: () => void;
		/** Whether the Yes/No question is showing; bindable so a keyboard shortcut can open it. */
		confirming?: boolean;
		/** 'popover' floats the question under the icon (tiles); 'inline' sits in the row (toolbar). */
		layout?: 'popover' | 'inline';
	}

	let { onconfirm, confirming = $bindable(false), layout = 'inline' }: Props = $props();

	function yes(e: Event) {
		e.stopPropagation();
		confirming = false;
		onconfirm();
	}

	function no(e: Event) {
		e.stopPropagation();
		confirming = false;
	}

	function ask(e: Event) {
		e.stopPropagation();
		confirming = true;
	}
</script>

<span class="delete-discussion {layout}" class:confirming>
	{#if !confirming}
		<button
			type="button"
			class="trash"
			title={DELETE_TOOLTIP}
			aria-label="Delete discussion"
			onclick={ask}
		>
			<svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
				<path d="M2.5 4h11M6 4V2.5h4V4M4 4l.6 9.2a1 1 0 0 0 1 .8h4.8a1 1 0 0 0 1-.8L12 4M6.5 6.5v5M9.5 6.5v5" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round" />
			</svg>
		</button>
	{:else}
		<span class="prompt" role="group" aria-label="Confirm delete">
			<span class="question">Delete?</span>
			<button type="button" class="yes" onclick={yes}>Yes</button>
			<button type="button" class="no" onclick={no}>No</button>
		</span>
	{/if}
</span>

<style>
	.delete-discussion {
		display: inline-flex;
		align-items: center;
		position: relative;
	}
	.trash {
		display: inline-flex;
		align-items: center;
		background: none;
		border: none;
		cursor: pointer;
		color: var(--color-text-muted);
		padding: 0.25rem;
		border-radius: 0.25rem;
	}
	.trash:hover,
	.trash:focus-visible {
		color: var(--color-error, #ef4444);
		background-color: var(--color-bg-secondary);
	}
	.prompt {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		white-space: nowrap;
	}
	.popover .prompt {
		position: absolute;
		top: 100%;
		right: 0;
		z-index: 5;
		padding: 0.25rem 0.5rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.375rem;
	}
	.question {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-error, #f87171);
	}
	.yes,
	.no {
		background: none;
		border: 1px solid;
		padding: 0.125rem 0.5rem;
		border-radius: 0.25rem;
		font-size: 0.7rem;
		font-weight: 600;
		cursor: pointer;
		font-family: inherit;
	}
	.yes {
		color: var(--color-error, #ef4444);
		border-color: var(--color-error, #ef4444);
	}
	.no {
		color: var(--color-text-muted);
		border-color: var(--color-border);
	}
</style>
