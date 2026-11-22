<script lang="ts" module>
	export type MenuEntry =
		| { kind: 'separator' }
		| { kind: 'item'; label: string; onselect: () => void; danger?: boolean }
		| { kind: 'check'; label: string; checked: boolean; onselect: () => void }
		| { kind: 'link'; label: string; href: string; download?: boolean };
</script>

<script lang="ts">
	import { tick } from 'svelte';
	import { tooltip } from '#lib/tooltip.js';

	interface Props {
		entries: MenuEntry[];
		/** What opening the menu offers, said in the tooltip of its button (text from ACTIONS). */
		tip: string;
		/** The button's picture: lines (the default), three dots for a row's actions, or sliders for view options. */
		icon?: 'menu' | 'more' | 'options';
		/** Names the menu for assistive technology. */
		label?: string;
	}

	let { entries, label = 'Actions', tip, icon = 'menu' }: Props = $props();

	let open = $state(false);
	let root = $state<HTMLElement | null>(null);
	let button = $state<HTMLButtonElement | null>(null);

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
		if (e.key === 'ArrowDown') {
			e.preventDefault();
			void openMenu();
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			void openMenu(true);
		}
	}

	function onMenuKey(e: KeyboardEvent) {
		const all = items();
		const at = all.indexOf(document.activeElement as HTMLElement);
		const move = (i: number) => {
			e.preventDefault();
			all[(i + all.length) % all.length]?.focus();
		};
		const moves: Record<string, () => void> = {
			ArrowDown: () => move(at + 1),
			ArrowUp: () => move(at - 1),
			Home: () => move(0),
			End: () => move(all.length - 1),
			Escape: () => {
				e.preventDefault();
				close();
			},
			Tab: () => close(false)
		};
		moves[e.key]?.();
	}

	function run(action: () => void) {
		close();
		action();
	}
</script>

<svelte:window onclick={(e) => { if (open && root && !root.contains(e.target as Node)) open = false; }} />

<div class="action-menu" bind:this={root}>
	<button
		type="button"
		class="menu-button"
		bind:this={button}
		use:tooltip={tip}
	
		aria-label={label}
		aria-haspopup="menu"
		aria-expanded={open}
		onclick={() => (open ? close(false) : openMenu())}
		onkeydown={onButtonKey}
	>
		<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
			{#if icon === 'more'}
				<circle cx="5" cy="12" r="1.4" fill="currentColor" /><circle cx="12" cy="12" r="1.4" fill="currentColor" /><circle cx="19" cy="12" r="1.4" fill="currentColor" />
			{:else if icon === 'options'}
				<line x1="4" y1="7" x2="20" y2="7" /><circle cx="9" cy="7" r="2" fill="var(--color-bg-secondary)" /><line x1="4" y1="17" x2="20" y2="17" /><circle cx="15" cy="17" r="2" fill="var(--color-bg-secondary)" />
			{:else}
				<line x1="4" y1="6" x2="20" y2="6" /><line x1="4" y1="12" x2="20" y2="12" /><line x1="4" y1="18" x2="20" y2="18" />
			{/if}
		</svg>
	</button>
	{#if open}
		<div class="menu" role="menu" aria-label={label} tabindex="-1" onkeydown={onMenuKey}>
			{#each entries as entry, i (i)}
				{#if entry.kind === 'separator'}
					<hr />
				{:else if entry.kind === 'check'}
					<button type="button" role="menuitemcheckbox" aria-checked={entry.checked} tabindex="-1" onclick={() => run(entry.onselect)}>
						<span class="check" aria-hidden="true">{#if entry.checked}<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12" /></svg>{/if}</span>{entry.label}
					</button>
				{:else if entry.kind === 'link'}
					<a role="menuitem" tabindex="-1" href={entry.href} download={entry.download ? '' : undefined} onclick={() => close(false)}><span class="check"></span>{entry.label}</a>
				{:else}
					<button type="button" role="menuitem" class:danger={entry.danger} tabindex="-1" onclick={() => run(entry.onselect)}><span class="check"></span>{entry.label}</button>
				{/if}
			{/each}
		</div>
	{/if}
</div>

<style>
	.action-menu { position: relative; display: inline-flex; }
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
		box-shadow: var(--shadow-popup);
	}
	.menu button, .menu a {
		display: flex; align-items: center; gap: 0.25rem; text-align: left; padding: 0.375rem 0.625rem;
		background: none; border: none; border-radius: 0.25rem; color: var(--color-text);
		font: inherit; font-size: 0.8rem; cursor: pointer; white-space: nowrap; text-decoration: none;
	}
	.menu button:hover, .menu a:hover, .menu button:focus-visible, .menu a:focus-visible { background: var(--color-bg-secondary); outline: none; }
	.menu button:focus-visible, .menu a:focus-visible { box-shadow: inset 0 0 0 1px var(--color-primary); }
	.menu .danger { color: var(--color-error); }
	.check { display: inline-block; width: 1rem; color: var(--color-primary); }
	hr { margin: 0.25rem 0; border: none; border-top: 1px solid var(--color-border); }
</style>
