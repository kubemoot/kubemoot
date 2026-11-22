<script lang="ts">
	import { tick } from 'svelte';
	import { askCommand, replayConversationCommand } from '#lib/cli-commands.js';

	interface Props {
		/** The span graph ("timeline") is shown. */
		showTimeline: boolean;
		showDebug: boolean;
		ontoggletimeline: () => void;
		ontoggledebug: () => void;
		oncopydiscussion: () => void;
		oncopythreadid: () => void;
		/** The export link for this conversation; absent when it cannot be downloaded. */
		downloadHref?: string | null;
		/** The conversation's crew, namespace and id, for the kmctl commands; absent without an id. */
		crew?: string;
		namespace?: string | null;
		conversationId?: string;
		/** Said in the tooltip of the button. */
		debugTitle?: string;
	}

	let {
		showTimeline, showDebug, ontoggletimeline, ontoggledebug, oncopydiscussion, oncopythreadid,
		downloadHref = null, crew, namespace, conversationId, debugTitle = 'Debug detail'
	}: Props = $props();

	let open = $state(false);
	let copied = $state('');
	let root = $state<HTMLElement | null>(null);
	let button = $state<HTMLButtonElement | null>(null);

	const kmctl = $derived(
		crew && conversationId
			? [
					{ label: 'Copy kmctl follow-up command', command: askCommand(crew, namespace, conversationId) },
					{ label: 'Copy kmctl replay command', command: replayConversationCommand(crew, conversationId, namespace) }
				]
			: []
	);

	function items(): HTMLElement[] {
		return root ? [...root.querySelectorAll<HTMLElement>('[role^="menuitem"]')] : [];
	}

	async function openMenu(focusLast = false) {
		open = true;
		await tick();
		const all = items();
		(focusLast ? all.at(-1) : all[0])?.focus();
	}

	function close(returnFocus = true) {
		open = false;
		if (returnFocus) button?.focus();
	}

	function onButtonKey(e: KeyboardEvent) {
		if (e.key === 'ArrowDown') { e.preventDefault(); void openMenu(); }
		else if (e.key === 'ArrowUp') { e.preventDefault(); void openMenu(true); }
	}

	function onMenuKey(e: KeyboardEvent) {
		const all = items();
		const at = all.indexOf(document.activeElement as HTMLElement);
		const move = (i: number) => { e.preventDefault(); all[(i + all.length) % all.length]?.focus(); };
		if (e.key === 'ArrowDown') move(at + 1);
		else if (e.key === 'ArrowUp') move(at - 1);
		else if (e.key === 'Home') move(0);
		else if (e.key === 'End') move(all.length - 1);
		else if (e.key === 'Escape') { e.preventDefault(); close(); }
		else if (e.key === 'Tab') close(false);
	}

	function run(action: () => void) {
		action();
		close();
	}

	async function copyCommand(item: { label: string; command: string }) {
		try {
			await navigator.clipboard.writeText(item.command);
			copied = item.label;
			setTimeout(() => { copied = ''; }, 1200);
		} catch {
			copied = '';
		}
		close();
	}
</script>

<svelte:window onclick={(e) => { if (open && root && !root.contains(e.target as Node)) open = false; }} />

<div class="discussion-menu" bind:this={root}>
	<button
		type="button"
		class="menu-button"
		bind:this={button}
		title="Actions"
		aria-label="Actions"
		aria-haspopup="menu"
		aria-expanded={open}
		onclick={() => (open ? close(false) : openMenu())}
		onkeydown={onButtonKey}
	>
		<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><line x1="4" y1="6" x2="20" y2="6" /><line x1="4" y1="12" x2="20" y2="12" /><line x1="4" y1="18" x2="20" y2="18" /></svg>
	</button>
	{#if open}
		<div class="menu" role="menu" aria-label="Discussion actions" tabindex="-1" onkeydown={onMenuKey}>
			<button type="button" role="menuitemcheckbox" aria-checked={showTimeline} tabindex="-1" onclick={() => run(ontoggletimeline)}>
				<span class="check" aria-hidden="true">{#if showTimeline}<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12" /></svg>{/if}</span>Show timeline
			</button>
			<button type="button" role="menuitemcheckbox" aria-checked={showDebug} tabindex="-1" title={debugTitle} onclick={() => run(ontoggledebug)}>
				<span class="check" aria-hidden="true">{#if showDebug}<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12" /></svg>{/if}</span>Debug detail
			</button>
			<hr />
			<button type="button" role="menuitem" tabindex="-1" onclick={() => run(oncopydiscussion)}><span class="check"></span>Copy discussion</button>
			<button type="button" role="menuitem" tabindex="-1" onclick={() => run(oncopythreadid)}><span class="check"></span>Copy thread id with the question</button>
			{#if downloadHref}
				<a role="menuitem" tabindex="-1" href={downloadHref} download onclick={() => close(false)}><span class="check"></span>Download conversation</a>
			{/if}
			{#if kmctl.length > 0}
				<hr />
				{#each kmctl as item (item.label)}
					<button type="button" role="menuitem" tabindex="-1" onclick={() => copyCommand(item)}><span class="check"></span>{copied === item.label ? 'Copied' : item.label}</button>
				{/each}
			{/if}
		</div>
	{/if}
</div>

<style>
	.discussion-menu { position: relative; display: inline-flex; }
	.menu-button {
		display: inline-flex; align-items: center; justify-content: center;
		width: 1.75rem; height: 1.75rem; padding: 0; background: none; border: none;
		border-radius: 0.25rem; cursor: pointer; opacity: 0.8; color: var(--color-text-muted);
	}
	.menu-button:hover, .menu-button[aria-expanded='true'] { opacity: 1; background-color: var(--color-bg-secondary); }
	.menu {
		position: absolute; right: 0; top: calc(100% + 0.25rem); z-index: 20; min-width: 15rem;
		display: flex; flex-direction: column; padding: 0.25rem;
		background: var(--color-bg, var(--color-bg-secondary)); border: 1px solid var(--color-border); border-radius: 0.375rem;
		box-shadow: 0 4px 12px rgba(0, 0, 0, 0.35);
	}
	.menu button, .menu a {
		display: flex; align-items: center; gap: 0.25rem; text-align: left; padding: 0.375rem 0.625rem;
		background: none; border: none; border-radius: 0.25rem; color: var(--color-text);
		font: inherit; font-size: 0.8rem; cursor: pointer; white-space: nowrap; text-decoration: none;
	}
	.menu button:hover, .menu a:hover, .menu button:focus-visible, .menu a:focus-visible { background: var(--color-bg-secondary); outline: none; }
	.menu button:focus-visible, .menu a:focus-visible { box-shadow: inset 0 0 0 1px var(--color-primary); }
	.check { display: inline-block; width: 1rem; color: var(--color-primary); }
	hr { margin: 0.25rem 0; border: none; border-top: 1px solid var(--color-border); }
</style>
