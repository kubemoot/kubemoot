<script lang="ts">
	import type { Snippet } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import YamlView from './YamlView.svelte';
	import type { YamlResource } from '#lib/yaml.js';
	import { TABS, tabFromUrl, tabUrl, nextTab, type ResourceTab } from '#lib/resource-tab.js';

	interface Props {
		/** The custom resource the page shows; the YAML tab renders it as Kubernetes returned it. */
		resource: YamlResource | null | undefined;
		/** The page's own view of the resource (the Overview tab). */
		children: Snippet;
	}

	// Pages wrap their own layout in this when they do not use DetailPanel, which wraps it for them.
	// The Overview stays mounted while hidden so its data, polling and open sections survive a tab switch.
	let { resource, children }: Props = $props();

	const active = $derived(tabFromUrl(page.url));
	let buttons = $state<Partial<Record<ResourceTab, HTMLButtonElement>>>({});

	function select(tab: ResourceTab) {
		if (tab === active) return;
		void goto(tabUrl(page.url, tab), { replace: true, reset: false });
	}

	function onKeydown(event: KeyboardEvent) {
		const target = nextTab(active, event.key);
		if (!target) return;
		event.preventDefault();
		select(target);
		buttons[target]?.focus();
	}
</script>

<div class="resource-tabs">
	<!-- Key presses bubble up from the tab buttons, which hold the focus. -->
	<div class="tablist" role="tablist" aria-label="View of this resource" tabindex="-1" onkeydown={onKeydown}>
		{#each TABS as tab (tab.id)}
			<button
				use:tooltip={ACTIONS.resourceTab(tab.id)}
				bind:this={buttons[tab.id]}
				type="button"
				role="tab"
				id="resource-tab-{tab.id}"
				class="tab"
				aria-selected={active === tab.id}
				aria-controls="resource-panel-{tab.id}"
				tabindex={active === tab.id ? 0 : -1}
				onclick={() => select(tab.id)}
			>
				{tab.label}
			</button>
		{/each}
	</div>

	<div role="tabpanel" id="resource-panel-overview" tabindex="0" aria-labelledby="resource-tab-overview" hidden={active !== 'overview'}>
		{@render children()}
	</div>
	<div role="tabpanel" id="resource-panel-yaml" tabindex="0" aria-labelledby="resource-tab-yaml" hidden={active !== 'yaml'}>
		{#if active === 'yaml'}
			<YamlView {resource} />
		{/if}
	</div>
</div>

<style>
	.tablist {
		display: flex;
		gap: 0.25rem;
		margin-bottom: 1.25rem;
		border-bottom: 1px solid var(--color-border);
	}

	.tab {
		padding: 0.5rem 1rem;
		margin-bottom: -1px;
		font-size: 0.9rem;
		font-weight: 500;
		background: none;
		border: none;
		border-bottom: 2px solid transparent;
		color: var(--color-text-muted);
		cursor: pointer;
	}

	.tab:hover {
		color: var(--color-text);
	}

	.tab[aria-selected='true'] {
		color: var(--color-text);
		border-bottom-color: var(--color-primary);
	}

	.tab:focus-visible {
		outline: 2px solid var(--color-primary);
		outline-offset: -2px;
	}
</style>
