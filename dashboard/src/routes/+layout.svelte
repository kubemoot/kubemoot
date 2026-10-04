<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { routeSection } from '#lib/route-section.js';
	import { page } from '$app/state';
	import { Sidebar } from '#lib/components/layout/index.js';
	import { NamespaceSelector } from '#lib/components/common/index.js';
	import { crewDirectory, dashboardMode, loadCrewDirectory, refresh } from '#lib/stores/index.js';

	// Pages where the crew selector doesn't apply. Discussions IS crew-scoped
	// (threads carry a crew), so it shows the selector and filters by crew.
	const NO_NAMESPACE_PAGES = new Set(['', 'nodes', 'messages', 'verify', 'config']);
	let showNamespace = $derived(() => {
		const section = routeSection(page.route.id);
		return section === null || !NO_NAMESPACE_PAGES.has(section);
	});

	interface Props {
		children: import('svelte').Snippet;
		data: { readOnly: boolean; scoped: boolean };
	}

	let { children, data }: Props = $props();

	// The mode is fixed per deployment, so setting the store while the component
	// initialises gives the server render and the hydrated page the same controls.
	// svelte-ignore state_referenced_locally
	dashboardMode.set({ readOnly: data.readOnly, scoped: data.scoped });

	let namespacesLoading = $state(true);
	let version = $state('...');

	// Global auto-refresh driver. The refresh store carries enabled + interval,
	// and pages re-fetch when $refreshTrigger changes - but nothing ticked it.
	// This is that ticker: a single app-wide interval so views are live, not
	// static (the dashboard is a realtime system). Reading enabled/interval via
	// $derived means trigger()'s lastRefresh updates don't reset the interval.
	let refreshEnabled = $derived($refresh.enabled);
	let refreshInterval = $derived($refresh.interval);
	$effect(() => {
		if (!refreshEnabled) return;
		const ticker = setInterval(() => refresh.trigger(), refreshInterval);
		return () => clearInterval(ticker);
	});

	onMount(async () => {
		// Fetch crews (kubemoot.ai/crew-labeled namespaces) for the selector
		await loadCrewDirectory(fetch, resolve('/api/namespaces'));
		namespacesLoading = false;

		// Fetch version
		try {
			const res = await fetch(resolve('/api/version'));
			const data = await res.json();
			version = data.version;
		} catch {
			version = 'dev';
		}
	});
</script>

<div class="app">
	<Sidebar {version} />

	<div class="main">
		<header class="topbar">
			<div class="topbar-left">
				{#if showNamespace()}
					<NamespaceSelector crews={$crewDirectory} loading={namespacesLoading} />
				{/if}
			</div>
			<div class="topbar-right">
			</div>
		</header>

		<main class="content">
			{@render children()}
		</main>
	</div>
</div>

<style>
	.app {
		display: flex;
		height: 100vh;
		overflow: hidden;
	}

	.main {
		flex: 1;
		display: flex;
		flex-direction: column;
		overflow: hidden;
	}

	.topbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 0.75rem 1.5rem;
		background-color: var(--color-bg-secondary);
		border-bottom: 1px solid var(--color-border);
		flex-shrink: 0;
	}

	.topbar-left,
	.topbar-right {
		display: flex;
		align-items: center;
		gap: 1rem;
	}

.content {
		flex: 1;
		overflow-y: auto;
		padding: 1.5rem;
	}
</style>
