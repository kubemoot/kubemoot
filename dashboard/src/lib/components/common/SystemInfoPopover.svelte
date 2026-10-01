<script lang="ts">
	import { resolve } from '$app/paths';
	import { onDestroy } from 'svelte';

	interface SystemInfo {
		operatorVersion: string;
		operatorImage: string;
		/** ISO-8601 timestamp of the running operator pod's startTime, or "". */
		operatorStartedAt: string;
	}

	let { dashboardVersion = '...' }: { dashboardVersion?: string } = $props();

	let open = $state(false);
	let info = $state<SystemInfo | null>(null);
	let loading = $state(false);
	let error = $state<string | null>(null);
	// Tick every minute while the popover is open so the uptime label
	// stays current without a re-fetch (the underlying pod doesn't move).
	let now = $state(Date.now());
	let tickHandle: ReturnType<typeof setInterval> | null = null;

	async function load() {
		if (info || loading) return;
		loading = true;
		try {
			const res = await fetch(resolve('/api/kubemoot/system-info'));
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			info = (await res.json()) as SystemInfo;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	function toggle(e: MouseEvent) {
		e.stopPropagation();
		open = !open;
		if (open) {
			load();
			now = Date.now();
			if (!tickHandle) {
				tickHandle = setInterval(() => { now = Date.now(); }, 60_000);
			}
		} else if (tickHandle) {
			clearInterval(tickHandle);
			tickHandle = null;
		}
	}

	function handleClickOutside(event: MouseEvent) {
		const target = event.target as HTMLElement;
		if (!target.closest('.sysinfo-wrapper')) {
			open = false;
			if (tickHandle) {
				clearInterval(tickHandle);
				tickHandle = null;
			}
		}
	}

	onDestroy(() => {
		if (tickHandle) clearInterval(tickHandle);
	});

	/**
	 * Format the uptime as the largest reasonable two-unit string:
	 * "3h 42m", "2d 5h", "45s" (under a minute), "1m 12s" (under 5 min).
	 * Keeps the popover line short while still letting the user notice
	 * a fresh restart (small numbers) vs. a long-running operator.
	 */
	const uptimeLabel = $derived.by(() => {
		if (!info?.operatorStartedAt) return null;
		const startedMs = new Date(info.operatorStartedAt).getTime();
		if (Number.isNaN(startedMs)) return null;
		const seconds = Math.max(0, Math.floor((now - startedMs) / 1000));
		if (seconds < 60) return `${seconds}s`;
		const minutes = Math.floor(seconds / 60);
		if (minutes < 5) return `${minutes}m ${seconds % 60}s`;
		if (minutes < 60) return `${minutes}m`;
		const hours = Math.floor(minutes / 60);
		if (hours < 24) return `${hours}h ${minutes % 60}m`;
		const days = Math.floor(hours / 24);
		return `${days}d ${hours % 24}h`;
	});

	const startedAtAbsolute = $derived.by(() => {
		if (!info?.operatorStartedAt) return '';
		try {
			return new Date(info.operatorStartedAt).toLocaleString();
		} catch {
			return info.operatorStartedAt;
		}
	});

	// Copy the component versions + uptime as a plain block for pasting into a
	// support request, so the user doesn't have to transcribe them by hand.
	let copied = $state(false);
	const parenthetical = (text: string | undefined) => (text ? ` (${text})` : '');

	async function copyInfo() {
		const operatorLine = info
			? `Operator:  v${info.operatorVersion}${parenthetical(info.operatorImage)}`
			: 'Operator:  unknown';
		const startedNote = startedAtAbsolute ? 'started ' + startedAtAbsolute : undefined;
		const uptimeLine = uptimeLabel
			? `Uptime:    ${uptimeLabel}${parenthetical(startedNote)}`
			: 'Uptime:    unknown';
		const lines = ['Kubemoot components', `Dashboard: v${dashboardVersion}`, operatorLine, uptimeLine];
		await navigator.clipboard.writeText(lines.join('\n'));
		copied = true;
		setTimeout(() => { copied = false; }, 2000);
	}
</script>

<svelte:document onclick={handleClickOutside} />

<span class="sysinfo-wrapper">
	<button class="sysinfo-btn" onclick={toggle} title="System info" aria-label="System info">
		<!-- Inline info icon (Feather-style) -->
		<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
			<circle cx="12" cy="12" r="10" />
			<line x1="12" y1="16" x2="12" y2="12" />
			<line x1="12" y1="8" x2="12.01" y2="8" />
		</svg>
	</button>
	{#if open}
		<div class="sysinfo-popup" onclick={(e) => e.stopPropagation()}>
			<div class="popup-header">
				<span>Kubemoot components</span>
				<button class="copy-btn" onclick={copyInfo} title="Copy versions & uptime for support" aria-label="Copy system info">
					{copied ? '✓ Copied' : '⧉ Copy'}
				</button>
			</div>
			<dl class="components">
				<dt>Dashboard</dt>
				<dd>v{dashboardVersion}</dd>
				<dt>Operator</dt>
				<dd>
					{#if loading}<span class="muted">loading…</span>
					{:else if error}<span class="muted" title={error}>lookup failed</span>
					{:else if info}v{info.operatorVersion}
					{:else}<span class="muted">unknown</span>
					{/if}
				</dd>
				<dt>Uptime</dt>
				<dd>
					{#if loading}<span class="muted">…</span>
					{:else if uptimeLabel}<span title={`Operator pod started ${startedAtAbsolute}`}>{uptimeLabel}</span>
					{:else if info && !info.operatorStartedAt}<span class="muted">no ready pod</span>
					{:else}<span class="muted">unknown</span>
					{/if}
				</dd>
			</dl>
		</div>
	{/if}
</span>

<style>
	.sysinfo-wrapper {
		position: relative;
		display: inline-flex;
		align-items: center;
	}

	.sysinfo-btn {
		width: 16px;
		height: 16px;
		background: transparent;
		border: none;
		color: var(--color-text-muted);
		cursor: pointer;
		padding: 0;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		opacity: 0.6;
		transition: opacity 0.15s, color 0.15s;
	}

	.sysinfo-btn:hover {
		opacity: 1;
		color: var(--color-primary);
	}

	.sysinfo-popup {
		position: absolute;
		top: calc(100% + 8px);
		left: 0;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		padding: 0.75rem;
		min-width: 220px;
		z-index: 100;
		box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
	}

	.popup-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		font-size: 0.7rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		margin-bottom: 0.5rem;
		opacity: 0.7;
	}

	.copy-btn {
		background: transparent;
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
		border-radius: 4px;
		cursor: pointer;
		font-size: 0.62rem;
		text-transform: none;
		letter-spacing: 0;
		padding: 0.1rem 0.4rem;
		white-space: nowrap;
	}

	.copy-btn:hover {
		color: var(--color-primary);
		border-color: var(--color-primary);
	}

	.components {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 0.25rem 0.75rem;
		margin: 0 0 0.6rem 0;
		font-size: 0.8rem;
	}

	.components dt {
		color: var(--color-text-muted);
	}

	.components dd {
		margin: 0;
		color: var(--color-text);
		font-family: var(--font-mono, monospace);
		font-size: 0.78rem;
	}

	.muted {
		color: var(--color-text-muted);
		font-style: italic;
	}

</style>
