<script lang="ts">
	import type { Snippet } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { ACTIONS } from '#lib/action-definitions.js';
	import ActionMenu, { type MenuEntry } from './ActionMenu.svelte';
	import CodeView from './CodeView.svelte';
	import { CopyFlash } from '#lib/copy-flash.svelte.js';
	import { hasManagedFields, type YamlResource } from '#lib/yaml.js';
	import { VIEWS, resourceLines, resourceText, viewFromUrl, viewUrl, type ResourceView, type TextFormat } from '#lib/resource-view.js';

	interface Props {
		/** The custom resource the page shows; the YAML and JSON views render it as Kubernetes returned it. */
		resource: YamlResource | null | undefined;
		/** The page's own view of the resource (the Description). */
		children: Snippet;
	}

	// Pages wrap their own layout in this when they do not use DetailPanel, which wraps it for them.
	// The Description stays mounted while hidden so its data, polling and open sections survive a switch.
	let { resource, children }: Props = $props();

	const active = $derived(viewFromUrl(page.url));
	const format = $derived<TextFormat>(active === 'description' ? 'yaml' : active);
	let showManaged = $state(false);
	const flash = new CopyFlash();

	const text = $derived(resource && active !== 'description' ? resourceText(resource, format, showManaged) : '');
	const lines = $derived(resourceLines(text, format));
	const hasManaged = $derived(resource ? hasManagedFields(resource) : false);

	function select(event: Event) {
		const next = (event.currentTarget as HTMLSelectElement).value as ResourceView;
		if (next !== active) void goto(viewUrl(page.url, next), { replace: true, reset: false });
	}

	// Copy and the managed fields only mean something for the text views.
	const options = $derived<MenuEntry[]>([
		{ kind: 'item', label: `Copy ${format.toUpperCase()}`, onselect: () => void flash.copy('view', text) },
		...(hasManaged ? [{ kind: 'check' as const, label: 'Show managed fields', checked: showManaged, onselect: () => (showManaged = !showManaged) }] : [])
	]);
</script>

<div class="resource-view">
	<div class="view-bar">
		{#if flash.copied}<span class="copied" role="status">Copied</span>{/if}
		{#if active !== 'description' && resource}
			<ActionMenu entries={options} label="View options" tip={ACTIONS.viewOptions} icon="options" />
		{/if}
		<select class="view-select" aria-label="View" value={active} onchange={select}>
			{#each VIEWS as view (view.id)}<option value={view.id}>{view.label}</option>{/each}
		</select>
	</div>

	<div id="resource-panel-description" hidden={active !== 'description'}>
		{@render children()}
	</div>
	{#if active !== 'description' && resource}
		<div id="resource-panel-{active}">
			<CodeView {lines} {format} />
		</div>
	{/if}
</div>

<style>
	.view-bar {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.5rem;
		margin-bottom: 0.75rem;
	}

	.view-select {
		padding: 0.2rem 1.4rem 0.2rem 0.6rem;
		font: inherit;
		font-size: 0.8rem;
		color: var(--color-text);
		background-color: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 0.3rem;
		cursor: pointer;
	}

	.view-select:hover {
		border-color: var(--color-primary);
	}

	.view-select:focus-visible {
		outline: 2px solid var(--color-primary);
		outline-offset: 2px;
	}

	.copied {
		font-size: 0.75rem;
		color: var(--color-success);
	}
</style>
