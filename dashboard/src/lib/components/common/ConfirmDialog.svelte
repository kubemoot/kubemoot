<script lang="ts">
	import type { Snippet } from 'svelte';
	import { onDestroy, onMount } from 'svelte';

	interface Props {
		/** The question the person answers, such as "Delete the fitness run gate-run?". */
		question: string;
		/** What goes with it, one line each. */
		goes: string[];
		/** A refusal or failure, in plain words; shown in red above the buttons. */
		refusal?: string | null;
		/** The delete is under way: the dialog cannot be dismissed and Delete cannot be pressed again. */
		busy?: boolean;
		/** The confirm button's words before anything has happened: "Delete", or "Stop and delete". */
		confirmLabel?: string;
		/** Whatever the question needs besides the list, such as a reason field. */
		extra?: Snippet;
		onconfirm: () => void;
		onclose: () => void;
		/** Keeps the confirm button off until the dialog has what it needs (a typed reason). */
		canConfirm?: boolean;
	}

	let { question, goes, refusal = null, busy = false, confirmLabel = 'Delete', extra, onconfirm, onclose, canConfirm = true }: Props = $props();

	let dialog = $state<HTMLElement | null>(null);
	const uid = $props.id();
	const titleId = `confirm-title-${uid}`;
	// Where the focus was when the dialog opened (the control that asked), so it can go back there.
	const opener = typeof document === 'undefined' ? null : document.activeElement;

	function buttonLabel(working: boolean, failed: boolean, idle: string): string {
		if (working) return 'Deleting...';
		return failed ? 'Try again' : idle;
	}
	const confirmText = $derived(buttonLabel(busy, refusal !== null, confirmLabel));

	// The dialog itself has the focus, not a button: Enter does nothing, and no button tooltip opens over the question.
	onMount(() => dialog?.focus());
	onDestroy(() => {
		if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
	});

	function dismiss() {
		if (!busy) onclose();
	}

	/** Tab stays inside the dialog: from the last control to the first, and from the first back to the last. */
	function trapTab(event: KeyboardEvent) {
		const controls = [...(dialog?.querySelectorAll<HTMLElement>('button:not(:disabled), input, [href]') ?? [])];
		if (controls.length === 0) return;
		const first = controls[0];
		const last = controls.at(-1) as HTMLElement;
		const active = document.activeElement;
		const leaving = event.shiftKey ? active === first || active === dialog : active === last;
		if (!leaving && dialog?.contains(active)) return;
		event.preventDefault();
		(event.shiftKey ? last : first).focus();
	}

	function onKeydown(event: KeyboardEvent) {
		if (event.key === 'Escape') {
			event.stopPropagation();
			dismiss();
		} else if (event.key === 'Tab') {
			trapTab(event);
		}
	}
</script>

<svelte:window onkeydown={onKeydown} />

<div class="backdrop" role="presentation" onpointerdown={(e) => e.target === e.currentTarget && dismiss()}>
	<div class="dialog" role="alertdialog" aria-modal="true" aria-labelledby={titleId} tabindex="-1" bind:this={dialog}>
		<h2 id={titleId}>{question}</h2>
		{#if goes.length > 0}
			<p class="lead">This deletes:</p>
			<ul>
				{#each goes as line (line)}<li>{line}</li>{/each}
			</ul>
		{/if}
		<p class="warn">This cannot be undone.</p>
		{#if extra}{@render extra()}{/if}
		{#if refusal}<p class="refusal" role="alert">{refusal}</p>{/if}
		<div class="buttons">
			<button type="button" disabled={busy} onclick={onclose}>{refusal ? 'Close' : 'Keep it'}</button>
			<button type="button" class="danger" disabled={busy || !canConfirm} onclick={() => !busy && canConfirm && onconfirm()}>{confirmText}</button>
		</div>
	</div>
</div>

<style>
	.backdrop {
		position: fixed; inset: 0; z-index: 50; display: flex; align-items: center; justify-content: center;
		background: rgba(0, 0, 0, 0.55); padding: 1rem;
	}
	.dialog:focus { outline: none; }
	.dialog {
		width: 28rem; max-width: 100%; padding: 1.1rem 1.25rem; background: var(--color-bg-secondary);
		border: 1px solid var(--color-border); border-radius: 0.5rem; box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
	}
	h2 { margin: 0 0 0.6rem; font-size: 1rem; overflow-wrap: anywhere; }
	.lead { margin: 0 0 0.25rem; color: var(--color-text-muted); font-size: 0.85rem; }
	ul { margin: 0 0 0.6rem; padding-left: 1.2rem; font-size: 0.85rem; }
	li { margin: 0.15rem 0; overflow-wrap: anywhere; }
	.warn { margin: 0 0 0.75rem; font-size: 0.8rem; color: var(--color-text-muted); }
	.refusal { margin: 0 0 0.75rem; padding: 0.4rem 0.6rem; font-size: 0.85rem; color: var(--color-error); border: 1px solid var(--color-error); border-radius: 0.3rem; }
	.buttons { display: flex; justify-content: flex-end; gap: 0.5rem; }
	button {
		background: none; border: 1px solid var(--color-border); border-radius: 0.3rem; padding: 0.3rem 0.9rem;
		color: var(--color-text); font: inherit; font-size: 0.85rem; cursor: pointer;
	}
	button:hover { border-color: var(--color-primary); }
	button:focus-visible { outline: 2px solid var(--color-primary); outline-offset: 2px; }
	button:disabled { opacity: 0.5; cursor: default; }
	.danger { border-color: var(--color-error); color: var(--color-error); }
	.danger:hover { background: rgba(239, 68, 68, 0.12); border-color: var(--color-error); }
</style>
