<script lang="ts">
	import { tick, type Snippet } from 'svelte';

	interface Props {
		/** What the icon opens, for a screen reader: "About Crews". */
		label: string;
		/** The lines of the note; the first is the term, the rest are quieter. */
		lines: readonly string[];
		/** Extra content after the lines, such as a link. */
		children?: Snippet;
	}

	let { label, lines, children }: Props = $props();

	let open = $state(false);
	let button = $state<HTMLButtonElement | null>(null);
	let popover = $state<HTMLElement | null>(null);
	let top = $state(0);
	let left = $state(0);

	const WIDTH_PX = 320;
	const EDGE_PX = 8;

	/** Opens below the icon, kept inside the window; fixed so no parent can clip it. */
	function place() {
		if (!button) return;
		const rect = button.getBoundingClientRect();
		const width = Math.min(WIDTH_PX, window.innerWidth - 2 * EDGE_PX);
		top = rect.bottom + 6;
		left = Math.max(EDGE_PX, Math.min(rect.left, window.innerWidth - width - EDGE_PX));
	}

	async function toggle() {
		open = !open;
		if (!open) return;
		place();
		await tick();
		popover?.focus();
	}

	function close(refocus: boolean) {
		open = false;
		if (refocus) button?.focus();
	}

	function onDocumentClick(event: MouseEvent) {
		if (!open) return;
		const target = event.target as Node;
		if (button?.contains(target) || popover?.contains(target)) return;
		close(false);
	}

	function onKey(event: KeyboardEvent) {
		if (open && event.key === 'Escape') {
			event.stopPropagation();
			close(true);
		}
	}
</script>

<svelte:document onclick={onDocumentClick} />
<svelte:window onkeydown={onKey} onresize={() => open && place()} onscrollcapture={() => open && close(false)} />

<button bind:this={button} type="button" class="info-btn" aria-label={label} aria-expanded={open} aria-haspopup="dialog" onclick={toggle}>i</button>
{#if open}
	<div bind:this={popover} class="info-popover" role="dialog" aria-label={label} tabindex="-1" style:top="{top}px" style:left="{left}px">
		{#each lines as line, i (i)}
			<p class:quiet={i > 0}>{line}</p>
		{/each}
		{#if children}{@render children()}{/if}
	</div>
{/if}

<style>
	.info-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
		width: 1.1rem;
		height: 1.1rem;
		margin: 2px;
		padding: 0;
		border-radius: 50%;
		border: 1px solid var(--color-text-muted);
		background: transparent;
		color: var(--color-text-muted);
		font: italic 700 0.7rem/1 Georgia, serif;
		cursor: pointer;
	}

	.info-btn:hover,
	.info-btn[aria-expanded='true'] {
		border-color: var(--color-primary);
		color: var(--color-primary);
	}

	.info-popover {
		position: fixed;
		z-index: 100;
		width: min(320px, calc(100vw - 16px));
		padding: 0.7rem 0.85rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		box-shadow: var(--shadow-popup);
		font-size: 0.8rem;
		line-height: 1.5;
		font-weight: 400;
		font-style: normal;
		color: var(--color-text);
		text-align: left;
	}

	.info-popover:focus {
		outline: none;
	}

	.info-popover p {
		margin: 0;
	}

	.info-popover p + p {
		margin-top: 0.4rem;
	}

	.quiet {
		color: var(--color-text-muted);
	}
</style>
