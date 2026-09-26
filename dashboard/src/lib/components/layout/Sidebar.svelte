<script lang="ts">
	import { page } from '$app/stores';
	import { base } from '$app/paths';
	import SystemInfoPopover from '$lib/components/common/SystemInfoPopover.svelte';

	const githubUrl = 'https://github.com/kubemoot/kubemoot';

	interface SidebarProps {
		version?: string;
	}

	let { version = '...' }: SidebarProps = $props();

	interface NavItem {
		href: string;
		label: string;
		icon: string;
		section?: string;
	}

	const navItems: NavItem[] = [
		{ href: `${base}/`, label: 'Overview', icon: 'grid' },
		{ href: `${base}/nodes`, label: 'Nodes', icon: 'server' },
		{ href: `${base}/discussions`, label: 'Discussions', icon: 'message-square', section: 'Messaging' },
		{ href: `${base}/messages`, label: 'Messages', icon: 'message-circle' },
		{ href: `${base}/crews`, label: 'Crews', icon: 'users', section: 'Crews' },
		{ href: `${base}/agents`, label: 'Agents', icon: 'cpu' },
		{ href: `${base}/promptmodules`, label: 'Prompts', icon: 'align-left' },
		{ href: `${base}/crew-memory`, label: 'Memory', icon: 'database' },
		{ href: `${base}/fitness`, label: 'Fitness', icon: 'activity' },
		{ href: `${base}/modelproviders`, label: 'Providers', icon: 'cloud', section: 'Models' },
		{ href: `${base}/models`, label: 'Models', icon: 'box' },
		{ href: `${base}/embeddingmodels`, label: 'Embeddings', icon: 'layers' },
		{ href: `${base}/mcpservers`, label: 'MCP Servers', icon: 'terminal', section: 'MCPs' },
		{ href: `${base}/mcpgateways`, label: 'MCP Gateways', icon: 'git-merge' },
		{ href: `${base}/mcpqualitypolicies`, label: 'Quality Policies', icon: 'shield' },
		{ href: `${base}/mcpcatalogs`, label: 'Catalogs', icon: 'book' },
		{ href: `${base}/mcpreports`, label: 'Reports', icon: 'file-text' },
		{ href: `${base}/ragsources`, label: 'RAG Sources', icon: 'database', section: 'Knowledge' },
		{ href: `${base}/config`, label: 'Config', icon: 'settings', section: 'System' }
	];

	function isActive(href: string, currentPath: string): boolean {
		if (href === `${base}/`) {
			return currentPath === `${base}` || currentPath === `${base}/`;
		}
		return currentPath.startsWith(href);
	}

</script>

<nav class="sidebar">
	<div class="logo">
		<span class="logo-icon">K</span>
		<div class="logo-info">
			<span class="logo-text">Kubemoot</span>
			<div class="logo-meta">
				<div class="version-link">
					<a href={githubUrl} target="_blank" rel="noopener noreferrer" class="version-github-link" title="View on GitHub">
						<span class="version-num">v{version}</span>
					</a>
					<SystemInfoPopover dashboardVersion={version} />
				</div>
			</div>
		</div>
	</div>

	<ul class="nav-list">
		{#each navItems as item, i}
			{#if item.section && (i === 0 || item.section !== navItems[i - 1]?.section)}
				<li class="nav-section">{item.section}</li>
			{/if}
			<li>
				<a
					href={item.href}
					class="nav-link"
					class:active={isActive(item.href, $page.url.pathname)}
				>
					<span class="nav-icon">
						<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							{#if item.icon === 'grid'}
								<rect x="3" y="3" width="7" height="7" /><rect x="14" y="3" width="7" height="7" /><rect x="3" y="14" width="7" height="7" /><rect x="14" y="14" width="7" height="7" />
							{:else if item.icon === 'server'}
								<rect x="2" y="2" width="20" height="8" rx="2" /><rect x="2" y="14" width="20" height="8" rx="2" /><circle cx="6" cy="6" r="1" fill="currentColor" /><circle cx="6" cy="18" r="1" fill="currentColor" />
							{:else if item.icon === 'cloud'}
								<path d="M18 10h-1.26A8 8 0 1 0 9 20h9a5 5 0 0 0 0-10z" />
							{:else if item.icon === 'box'}
								<path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z" /><polyline points="3.27 6.96 12 12.01 20.73 6.96" /><line x1="12" y1="22.08" x2="12" y2="12" />
							{:else if item.icon === 'layers'}
								<polygon points="12 2 2 7 12 12 22 7 12 2" /><polyline points="2 17 12 22 22 17" /><polyline points="2 12 12 17 22 12" />
							{:else if item.icon === 'terminal'}
								<polyline points="4 17 10 11 4 5" /><line x1="12" y1="19" x2="20" y2="19" />
							{:else if item.icon === 'git-merge'}
								<circle cx="18" cy="18" r="3" /><circle cx="6" cy="6" r="3" /><path d="M6 21V9a9 9 0 0 0 9 9" />
							{:else if item.icon === 'database'}
								<ellipse cx="12" cy="5" rx="9" ry="3" /><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3" /><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5" />
							{:else if item.icon === 'cpu'}
								<rect x="4" y="4" width="16" height="16" rx="2" /><rect x="9" y="9" width="6" height="6" /><line x1="9" y1="1" x2="9" y2="4" /><line x1="15" y1="1" x2="15" y2="4" /><line x1="9" y1="20" x2="9" y2="23" /><line x1="15" y1="20" x2="15" y2="23" /><line x1="20" y1="9" x2="23" y2="9" /><line x1="20" y1="14" x2="23" y2="14" /><line x1="1" y1="9" x2="4" y2="9" /><line x1="1" y1="14" x2="4" y2="14" />
							{:else if item.icon === 'shield'}
								<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
							{:else if item.icon === 'book'}
								<path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20" /><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z" />
							{:else if item.icon === 'file-text'}
							<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" /><polyline points="14 2 14 8 20 8" /><line x1="16" y1="13" x2="8" y2="13" /><line x1="16" y1="17" x2="8" y2="17" /><polyline points="10 9 9 9 8 9" />
						{:else if item.icon === 'share-2'}
							<circle cx="18" cy="5" r="3" /><circle cx="6" cy="12" r="3" /><circle cx="18" cy="19" r="3" /><line x1="8.59" y1="13.51" x2="15.42" y2="17.49" /><line x1="15.41" y1="6.51" x2="8.59" y2="10.49" />
						{:else if item.icon === 'message-square'}
							<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
						{:else if item.icon === 'message-circle'}
							<path d="M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z" />
						{:else if item.icon === 'users'}
								<path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2" /><circle cx="9" cy="7" r="4" /><path d="M23 21v-2a4 4 0 0 0-3-3.87" /><path d="M16 3.13a4 4 0 0 1 0 7.75" />
							{:else if item.icon === 'activity'}
								<polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
							{:else if item.icon === 'align-left'}
								<line x1="17" y1="10" x2="3" y2="10" /><line x1="21" y1="6" x2="3" y2="6" /><line x1="21" y1="14" x2="3" y2="14" /><line x1="17" y1="18" x2="3" y2="18" />
							{:else if item.icon === 'check-circle'}
								<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14" /><polyline points="22 4 12 14.01 9 11.01" />
							{:else if item.icon === 'settings'}
								<circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z" />
							{/if}
						</svg>
					</span>
					<span class="nav-label">{item.label}</span>
				</a>
			</li>
		{/each}
	</ul>

	<div class="sidebar-footer">
		<span class="powered-by" title={'AI can make mistakes.\nVerify important info.'}>
			Powered by Kubemoot
		</span>
	</div>
</nav>

<style>
	.sidebar {
		width: 220px;
		background-color: var(--color-bg-secondary);
		border-right: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		padding: 1rem;
		flex-shrink: 0;
	}

	.logo {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.5rem;
		margin-bottom: 1.5rem;
	}

	.logo-icon {
		width: 36px;
		height: 36px;
		background: linear-gradient(135deg, var(--color-primary), var(--color-purple));
		border-radius: 8px;
		display: flex;
		align-items: center;
		justify-content: center;
		font-weight: 700;
		font-size: 1.25rem;
		color: white;
	}

	.logo-info {
		display: flex;
		flex-direction: column;
	}

	.logo-text {
		font-size: 1.125rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.logo-meta {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-top: 0.25rem;
	}

	.logo-tagline {
		font-size: 0.7rem;
		color: var(--color-text-muted);
	}

	.version-link {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.version-num {
		font-size: 0.65rem;
		color: var(--color-text-muted);
		opacity: 0.7;
	}

	.version-github-link {
		text-decoration: none;
		transition: opacity 0.2s;
	}

	.version-github-link:hover {
		text-decoration: none;
	}

	.version-github-link:hover .version-num {
		opacity: 1;
	}

	.nav-list {
		list-style: none;
		flex: 1;
		overflow-y: auto;
	}

	.nav-section {
		font-size: 0.65rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		padding: 1rem 0.75rem 0.5rem;
		margin-top: 0.5rem;
	}

	.nav-section:first-child {
		margin-top: 0;
		padding-top: 0;
	}

	.nav-link {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.625rem 0.75rem;
		border-radius: 0.5rem;
		color: var(--color-text-muted);
		transition: all 0.15s;
		font-size: 0.875rem;
	}

	.nav-link:hover {
		background-color: var(--color-bg-tertiary);
		color: var(--color-text);
		text-decoration: none;
	}

	.nav-link.active {
		background-color: var(--color-primary);
		color: white;
	}

	.nav-link.active:hover {
		background-color: var(--color-primary-hover);
	}

	.nav-icon {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 20px;
		height: 20px;
	}

	.sidebar-footer {
		margin-top: 1rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--color-border);
		text-align: center;
	}

	.powered-by {
		font-size: 0.7rem;
		color: var(--color-text-muted);
		cursor: help;
	}

</style>
