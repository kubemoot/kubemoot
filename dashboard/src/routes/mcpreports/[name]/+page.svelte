<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { resolve } from '$app/paths';
	import { namespace as nsStore, refreshTrigger } from '$stores';
	import type { MCPServerReport } from '$types/kubemoot.js';

	let report = $state<MCPServerReport | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let editing = $state(false);
	let editVerdict = $state('');
	let editNotes = $state('');
	let editAuthor = $state('');

	const name = $derived($page.params.name as string);
	const namespace = $derived($page.url.searchParams.get('namespace') || $nsStore);

	async function fetchReport() {
		loading = true;
		error = null;
		try {
			const res = await fetch(`${resolve('/api/kubemoot/mcpserverreports/[name]', { name })}?namespace=${namespace}`);
			const data = await res.json();
			if (data.error) throw new Error(data.error);
			report = data;
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch report';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchReport();
	});

	$effect(() => {
		$refreshTrigger;
		fetchReport();
	});

	function startEdit() {
		if (!report) return;
		editing = true;
		editVerdict = report.spec?.adminVerdict || '';
		editNotes = report.spec?.adminNotes || '';
		editAuthor = report.spec?.adminAuthor || '';
	}

	async function saveEdit() {
		if (!report) return;
		try {
			const res = await fetch(`${resolve('/api/kubemoot/mcpserverreports/[name]', { name })}?namespace=${namespace}`, {
				method: 'PATCH',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					adminVerdict: editVerdict,
					adminNotes: editNotes,
					adminAuthor: editAuthor
				})
			});
			if (!res.ok) throw new Error('Failed to update');
			editing = false;
			await fetchReport();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to save';
		}
	}

	function verdictColor(verdict: string): string {
		switch (verdict) {
			case 'use': return 'var(--color-success, #22c55e)';
			case 'caution': return 'var(--color-warning, #eab308)';
			case 'avoid': return 'var(--color-error, #ef4444)';
			default: return 'var(--color-text-muted, #6b7280)';
		}
	}

	function phaseIcon(phase: string): string {
		switch (phase) {
			case 'deploy': return 'D';
			case 'connect': return 'C';
			case 'initialize': return 'I';
			case 'discover': return 'T';
			case 'call': return 'X';
			default: return '?';
		}
	}
</script>

<div class="detail-page">
	<div class="header">
		<a href="{resolve('/mcpreports')}" class="back-link">Back to Reports</a>
		<h1>{name}</h1>
	</div>

	{#if loading}
		<div class="loading">Loading report...</div>
	{:else if error}
		<div class="error">{error}</div>
	{:else if report}
		<div class="sections">
			<div class="section">
				<h2>Status</h2>
				<div class="info-grid">
					<div class="info-item">
						<span class="info-label">Verdict</span>
						<span class="verdict-badge" style="background: {verdictColor(report.status?.verdict || 'untested')}">
							{report.status?.verdict || 'untested'}
						</span>
					</div>
					<div class="info-item">
						<span class="info-label">Success Rate</span>
						<span>{report.status?.successRate || 'N/A'}</span>
					</div>
					<div class="info-item">
						<span class="info-label">Transport</span>
						<span>{report.status?.recommendedTransport || '-'}</span>
					</div>
					<div class="info-item">
						<span class="info-label">Version</span>
						<span>{report.status?.recommendedVersion || '-'}</span>
					</div>
					<div class="info-item">
						<span class="info-label">Success / Fail</span>
						<span>{report.status?.successCount || 0} / {report.status?.failureCount || 0}</span>
					</div>
					<div class="info-item">
						<span class="info-label">Last Tested</span>
						<span>{report.status?.lastTested ? new Date(report.status.lastTested).toLocaleString() : '-'}</span>
					</div>
				</div>
			</div>

			<div class="section">
				<h2>Spec</h2>
				<div class="info-grid">
					<div class="info-item">
						<span class="info-label">Server Name</span>
						<span>{report.spec?.serverName || '-'}</span>
					</div>
					<div class="info-item">
						<span class="info-label">Registry Type</span>
						<span>{report.spec?.registryType || '-'}</span>
					</div>
					{#if report.spec?.githubUrl}
						<div class="info-item full-width">
							<span class="info-label">GitHub</span>
							<a href={report.spec.githubUrl} target="_blank" rel="noopener">{report.spec.githubUrl}</a>
						</div>
					{/if}
				</div>
			</div>

			{#if report.status?.chroniclerNotes}
				<div class="section">
					<h2>Chronicler Notes</h2>
					<p class="chronicler-notes">{report.status.chroniclerNotes}</p>
				</div>
			{/if}

			<div class="section">
				<h2>Admin Curation</h2>
				{#if editing}
					<div class="edit-form">
						<div class="form-field">
							<label for="edit-verdict">Pin Verdict:</label>
							<select id="edit-verdict" bind:value={editVerdict}>
								<option value="">Auto (computed)</option>
								<option value="use">Use</option>
								<option value="caution">Caution</option>
								<option value="avoid">Avoid</option>
							</select>
						</div>
						<div class="form-field">
							<label for="edit-notes">Notes:</label>
							<textarea id="edit-notes" bind:value={editNotes} rows="3" placeholder="Why are you pinning this verdict?"></textarea>
						</div>
						<div class="form-field">
							<label for="edit-author">Author:</label>
							<input id="edit-author" bind:value={editAuthor} placeholder="Your name" />
						</div>
						<div class="form-actions">
							<button class="btn-save" onclick={saveEdit}>Save</button>
							<button class="btn-cancel" onclick={() => editing = false}>Cancel</button>
						</div>
					</div>
				{:else}
					<div class="admin-info">
						{#if report.spec?.adminVerdict}
							<div><strong>Pinned:</strong> {report.spec.adminVerdict} by {report.spec.adminAuthor || 'unknown'}</div>
							<div><strong>Reason:</strong> {report.spec.adminNotes || '-'}</div>
						{:else}
							<div class="muted">No admin override. Verdict is auto-computed from trial history.</div>
						{/if}
						<button class="btn-edit" onclick={startEdit}>Edit</button>
					</div>
				{/if}
			</div>

			<div class="section">
				<h2>Trial History ({report.status?.trials?.length || 0})</h2>
				{#if report.status?.trials?.length}
					<div class="trials">
						{#each [...(report.status.trials)].reverse() as trial}
							<div class="trial" class:success={trial.success} class:failure={!trial.success}>
								<span class="trial-phase" title={trial.phase}>{phaseIcon(trial.phase)}</span>
								<span class="trial-result">{trial.success ? 'OK' : 'FAIL'}</span>
								<span class="trial-transport">{trial.transport}</span>
								<span class="trial-version">{trial.version || '-'}</span>
								<span class="trial-date">
									{#if trial.testedAt}
										{new Date(trial.testedAt).toLocaleString()}
									{/if}
								</span>
								{#if trial.toolsFound > 0}
									<span class="trial-tools">{trial.toolsFound} tools</span>
								{/if}
								{#if trial.errorMessage}
									<div class="trial-error">{trial.errorMessage}</div>
								{/if}
							</div>
						{/each}
					</div>
				{:else}
					<div class="muted">No trials recorded yet.</div>
				{/if}
			</div>
		</div>
	{/if}
</div>

<style>
	.detail-page {
		padding: 1.5rem;
		max-width: 900px;
	}

	.header { margin-bottom: 1.5rem; }

	.back-link {
		font-size: 0.8rem;
		color: var(--color-primary);
		text-decoration: none;
	}

	.back-link:hover { text-decoration: underline; }

	h1 {
		font-size: 1.25rem;
		font-weight: 600;
		margin: 0.5rem 0 0;
	}

	.loading, .error {
		text-align: center;
		padding: 2rem;
		color: var(--color-text-muted);
	}

	.error { color: var(--color-error, #ef4444); }

	.sections {
		display: flex;
		flex-direction: column;
		gap: 1.5rem;
	}

	.section {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 8px;
		padding: 1rem;
	}

	h2 {
		font-size: 0.8rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		margin: 0 0 0.75rem;
	}

	.info-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.75rem;
	}

	.info-item { font-size: 0.875rem; }
	.info-item.full-width { grid-column: 1 / -1; }

	.info-label {
		display: block;
		font-size: 0.7rem;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.03em;
		margin-bottom: 0.2rem;
	}

	.info-grid a {
		color: var(--color-primary);
		text-decoration: none;
	}

	.verdict-badge {
		display: inline-block;
		padding: 0.15rem 0.5rem;
		border-radius: 9999px;
		font-size: 0.7rem;
		font-weight: 600;
		color: #000;
		text-transform: uppercase;
	}

	.chronicler-notes {
		font-size: 0.85rem;
		line-height: 1.5;
		background: var(--color-bg);
		padding: 0.75rem;
		border-radius: 4px;
		margin: 0;
	}

	.edit-form { display: flex; flex-direction: column; gap: 0.5rem; }
	.form-field { display: flex; flex-direction: column; gap: 0.2rem; }
	.form-field label { font-size: 0.75rem; color: var(--color-text-muted); }
	.form-field select, .form-field input, .form-field textarea {
		background: var(--color-bg);
		color: var(--color-text);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		padding: 0.4rem;
		font-size: 0.85rem;
		font-family: inherit;
	}
	.form-actions { display: flex; gap: 0.5rem; margin-top: 0.25rem; }
	.btn-save, .btn-cancel, .btn-edit {
		padding: 0.35rem 0.75rem;
		border-radius: 4px;
		border: 1px solid var(--color-border);
		font-size: 0.8rem;
		cursor: pointer;
	}
	.btn-save { background: var(--color-success, #22c55e); color: #000; border-color: var(--color-success, #22c55e); }
	.btn-cancel { background: var(--color-bg-secondary); color: var(--color-text); }
	.btn-edit { background: var(--color-bg-secondary); color: var(--color-text); }
	.btn-edit:hover { background: var(--color-bg-tertiary); }

	.admin-info { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.85rem; }
	.admin-info .btn-edit { align-self: flex-start; margin-top: 0.5rem; }

	.muted { color: var(--color-text-muted); font-size: 0.85rem; }

	.trials { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; font-family: monospace; }
	.trial { display: flex; gap: 0.5rem; align-items: center; padding: 0.3rem 0.5rem; border-radius: 4px; background: var(--color-bg); flex-wrap: wrap; }
	.trial.success { border-left: 3px solid var(--color-success, #22c55e); }
	.trial.failure { border-left: 3px solid var(--color-error, #ef4444); }
	.trial-phase { display: inline-flex; align-items: center; justify-content: center; width: 1.2rem; height: 1.2rem; border-radius: 3px; background: var(--color-bg-secondary); font-size: 0.65rem; font-weight: bold; }
	.trial-result { font-weight: 600; min-width: 2.5rem; }
	.trial.success .trial-result { color: var(--color-success, #22c55e); }
	.trial.failure .trial-result { color: var(--color-error, #ef4444); }
	.trial-transport { color: var(--color-text-muted); min-width: 3rem; }
	.trial-version { color: var(--color-text-muted); min-width: 4rem; }
	.trial-date { color: var(--color-text-muted); font-size: 0.7rem; }
	.trial-tools { color: var(--color-success, #22c55e); font-size: 0.7rem; }
	.trial-error { color: var(--color-error, #ef4444); font-size: 0.7rem; width: 100%; margin-top: 0.25rem; }
</style>
