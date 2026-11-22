<script lang="ts">
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import '../app.css';
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { showsCrewSelector } from '#lib/route-section.js';
	import { page } from '$app/state';
	import { Sidebar } from '#lib/components/layout/index.js';
	import { NamespaceSelector } from '#lib/components/common/index.js';
	import { crewDirectory, dashboardMode, loadCrewDirectory, refresh } from '#lib/stores/index.js';
	import { fetchDashboardVersion } from '#lib/dashboard-version.js';

	let showNamespace = $derived(showsCrewSelector(page.route.id));

	interface Props {
		children: import('svelte').Snippet;
		data: { scoped: boolean; anonymousReadOnly: boolean; user: { username: string } | null };
	}

	let { children, data }: Props = $props();

	// The mode is fixed per deployment, so setting the store while the component
	// initialises gives the server render and the hydrated page the same controls.
	// svelte-ignore state_referenced_locally
	dashboardMode.set({ scoped: data.scoped });

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

	// The sign-in page stands alone: no data is fetched for it.
	const bare = $derived(page.route.id === '/signin');

	onMount(async () => {
		if (page.route.id === '/signin') return;
		// Fetch crews (kubemoot.ai/crew-labeled namespaces) for the selector
		await loadCrewDirectory(fetch, resolve('/api/namespaces'));
		namespacesLoading = false;

		// Fetch version
		version = await fetchDashboardVersion(fetch, resolve('/api/version'));
	});
</script>

{#if bare}
	{@render children()}
{:else}
<div class="app">
	<Sidebar {version} />

	<div class="main">
		<header class="topbar">
			<div class="topbar-left">
				{#if showNamespace}
					<NamespaceSelector crews={$crewDirectory} loading={namespacesLoading} />
				{/if}
			</div>
			<div class="topbar-right">
				{#if data.user}
					<span class="who" title="You are signed in with your own Kubernetes token">Signed in as <strong>{data.user.username}</strong></span>
					<form method="POST" action={resolve('/signout')}>
						<button type="submit" class="signout" use:tooltip={ACTIONS.signOut}>Sign out</button>
					</form>
				{:else if data.anonymousReadOnly}
					<span class="who">Read-only view</span>
					<a class="signin-link" href={resolve('/signin')}>Sign in</a>
				{/if}
			</div>
		</header>

		<main class="content">
			{@render children()}
		</main>
	</div>
</div>
{/if}

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

	.who {
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.who strong {
		color: var(--color-text);
	}

	.signout {
		background: transparent;
		border: 1px solid var(--color-border);
		border-radius: 6px;
		color: var(--color-text);
		padding: 0.25rem 0.75rem;
		font-size: 0.8rem;
	}

	.signin-link {
		font-size: 0.875rem;
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
