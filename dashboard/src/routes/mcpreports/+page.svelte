<script lang="ts">
	import { reportComparator } from '#lib/mcp-report-sort.js';
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { readOnly, refreshTrigger } from '#lib/stores/index.js';
	import { ResourceList } from '#lib/components/layout/index.js';
	import { HelpTooltip } from '#lib/components/common/index.js';
	import type { MCPServerReport } from '#lib/types/kubemoot.js';

	let reports = $state<MCPServerReport[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let expandedReport = $state<string | null>(null);
	let editingReport = $state<string | null>(null);
	let editVerdict = $state('');
	let editNotes = $state('');
	let editAuthor = $state('');
	let filterVerdict = $state('all');
	let sortBy = $state('lastTested');

	async function fetchReports() {
		loading = true;
		error = null;
		try {
			const res = await fetch(resolve('/api/kubemoot/mcpserverreports'));
			const data = await res.json();
			if (data.error) throw new Error(data.error);
			reports = data.items || [];
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch reports';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchReports();
	});

	$effect(() => {
		$refreshTrigger;
		fetchReports();
	});

	function toggleExpand(name: string) {
		expandedReport = expandedReport === name ? null : name;
	}

	function startEdit(report: MCPServerReport) {
		editingReport = report.metadata.name;
		editVerdict = report.spec?.adminVerdict || '';
		editNotes = report.spec?.adminNotes || '';
		editAuthor = report.spec?.adminAuthor || '';
	}

	function cancelEdit() {
		editingReport = null;
	}

	async function saveEdit(report: MCPServerReport) {
		try {
			const res = await fetch(resolve('/api/kubemoot/mcpserverreports/[name]', { name: report.metadata.name }), {
				method: 'PATCH',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					adminVerdict: editVerdict,
					adminNotes: editNotes,
					adminAuthor: editAuthor
				})
			});
			if (!res.ok) throw new Error('Failed to update report');
			editingReport = null;
			await fetchReports();
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

	const filteredReports = $derived(
		reports
			.filter(r => filterVerdict === 'all' || r.status?.verdict === filterVerdict)
			.sort(reportComparator(sortBy))
	);
</script>

<ResourceList
	title="MCP Server Reports"
	count={filteredReports.length}
	{loading}
	{error}
	helpText="MCP Server Reports track the test history and reliability verdict for each MCP server. Verdicts are auto-computed from trial results."
>
	{#snippet actions()}
		<div class="controls">
			<div class="filter">
				<label for="verdict-filter">Filter:</label>
				<select id="verdict-filter" bind:value={filterVerdict}>
					<option value="all">All Verdicts</option>
					<option value="use">Use</option>
					<option value="caution">Caution</option>
					<option value="avoid">Avoid</option>
					<option value="untested">Untested</option>
				</select>
			</div>
			<div class="sort">
				<label for="sort-by">Sort:</label>
				<select id="sort-by" bind:value={sortBy}>
					<option value="lastTested">Last Tested</option>
					<option value="successRate">Success Rate</option>
					<option value="name">Name</option>
				</select>
			</div>
		</div>
	{/snippet}

	{#snippet children()}
		<div class="reports-table">
			<div class="table-header">
				<span class="col-name">Server</span>
				<span class="col-verdict">
					Verdict <HelpTooltip text="use = reliable, passed all tests. caution = some failures, use carefully. avoid = mostly failing, not recommended. untested = no test data." />
				</span>
				<span class="col-rate">Success%</span>
				<span class="col-transport">
					Transport <HelpTooltip text="stdio = standard I/O via MCP Bridge sidecar (recommended for most servers). http = direct HTTP/SSE connection." />
				</span>
				<span class="col-tested">Last Tested</span>
				<span class="col-admin">
					Admin <HelpTooltip text="auto = verdict computed from trial history. pinned = admin manually set the verdict override." />
				</span>
			</div>

			{#each filteredReports as report (report.metadata.namespace + '/' + report.metadata.name)}
				<div class="table-row" class:expanded={expandedReport === report.metadata.name}>
					<button class="row-main" onclick={() => toggleExpand(report.metadata.name)}>
						<span class="col-name">
							{#if report.spec?.githubUrl}
								<a href={report.spec.githubUrl} target="_blank" rel="noopener" class="server-link" onclick={(e) => e.stopPropagation()}>
									{report.spec?.serverName || report.metadata.name}
								</a>
							{:else}
								{report.spec?.serverName || report.metadata.name}
							{/if}
						</span>
						<span class="col-verdict">
							<span class="verdict-badge" style="background: {verdictColor(report.status?.verdict || 'untested')}">
								{report.status?.verdict || 'untested'}
							</span>
						</span>
						<span class="col-rate">{report.status?.successRate || 'N/A'}</span>
						<span class="col-transport">{report.status?.recommendedTransport || '-'}</span>
						<span class="col-tested">
							{#if report.status?.lastTested}
								{new Date(report.status.lastTested).toLocaleDateString()}
							{:else}
								-
							{/if}
						</span>
						<span class="col-admin">
							{#if report.spec?.adminVerdict}
								<span class="admin-pin" style="color: {verdictColor(report.spec.adminVerdict)}">pinned</span>
							{:else}
								auto
							{/if}
						</span>
					</button>

					{#if expandedReport === report.metadata.name}
						<div class="row-detail">
							<div class="detail-sections">
								<div class="detail-section">
									<h3>Summary</h3>
									<div class="detail-grid">
										<div><strong>Version:</strong> {report.status?.recommendedVersion || '-'}</div>
										<div><strong>Transport:</strong> {report.status?.recommendedTransport || '-'}</div>
										<div><strong>Registry:</strong> {report.spec?.registryType || '-'}</div>
										<div><strong>Success:</strong> {report.status?.successCount || 0} / Fail: {report.status?.failureCount || 0}</div>
										{#if report.spec?.githubUrl}
											<div><strong>GitHub:</strong> <a href={report.spec.githubUrl} target="_blank" rel="noopener">{report.spec.githubUrl}</a></div>
										{/if}
									</div>
								</div>

								{#if report.status?.chroniclerNotes}
									<div class="detail-section">
										<h3>Chronicler Notes</h3>
										<p class="chronicler-notes">{report.status.chroniclerNotes}</p>
									</div>
								{/if}

								<div class="detail-section">
									<h3>Admin Curation</h3>
									{#if editingReport === report.metadata.name && !$readOnly}
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
												<textarea id="edit-notes" bind:value={editNotes} rows="2" placeholder="Why are you pinning this verdict?"></textarea>
											</div>
											<div class="form-field">
												<label for="edit-author">Author:</label>
												<input id="edit-author" bind:value={editAuthor} placeholder="Your name" />
											</div>
											<div class="form-actions">
												<button class="btn-save" onclick={() => saveEdit(report)}>Save</button>
												<button class="btn-cancel" onclick={cancelEdit}>Cancel</button>
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
											{#if !$readOnly}
												<button class="btn-edit" onclick={() => startEdit(report)}>Edit</button>
											{/if}
										</div>
									{/if}
								</div>

								<div class="detail-section">
									<h3>Trial History ({report.status?.trials?.length || 0})</h3>
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
														<span class="trial-error" title={trial.errorMessage}>
															{trial.errorMessage.substring(0, 80)}{trial.errorMessage.length > 80 ? '...' : ''}
														</span>
													{/if}
												</div>
											{/each}
										</div>
									{:else}
										<div class="muted">No trials recorded yet.</div>
									{/if}
								</div>
							</div>
						</div>
					{/if}
				</div>
			{/each}
		</div>
	{/snippet}

	{#snippet empty()}
		<p>No MCP server reports found. Deploy some MCP servers to start building experience.</p>
	{/snippet}
</ResourceList>

<style>
	.controls {
		display: flex;
		gap: 1rem;
		align-items: center;
		flex-wrap: wrap;
	}

	.controls label {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		margin-right: 0.25rem;
	}

	.controls select {
		background: var(--color-bg-secondary);
		color: var(--color-text);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		padding: 0.35rem 0.5rem;
		font-size: 0.8rem;
	}

	.reports-table {
		border: 1px solid var(--color-border);
		border-radius: 8px;
		overflow: hidden;
		width: 100%;
	}

	.table-header {
		display: grid;
		grid-template-columns: minmax(180px, 3fr) minmax(100px, 1fr) minmax(80px, 1fr) minmax(90px, 1fr) minmax(90px, 1fr) 70px;
		column-gap: 0.75rem;
		padding: 0.5rem 1rem;
		font-size: 0.7rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		background: var(--color-bg-secondary);
		border-bottom: 1px solid var(--color-border);
		align-items: center;
	}

	.table-header > span {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.table-row {
		border-bottom: 1px solid var(--color-border);
	}

	.table-row:last-child { border-bottom: none; }

	.row-main {
		display: grid;
		grid-template-columns: minmax(180px, 3fr) minmax(100px, 1fr) minmax(80px, 1fr) minmax(90px, 1fr) minmax(90px, 1fr) 70px;
		column-gap: 0.75rem;
		width: 100%;
		padding: 0.65rem 1rem;
		background: none;
		border: none;
		color: var(--color-text);
		cursor: pointer;
		font-size: 0.85rem;
		text-align: left;
		align-items: center;
	}

	.row-main > span {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.row-main:hover { background: var(--color-bg-tertiary); }

	.server-link {
		color: var(--color-primary);
		text-decoration: none;
	}

	.server-link:hover {
		text-decoration: underline;
	}

	.verdict-badge {
		display: inline-block;
		padding: 0.15rem 0.5rem;
		border-radius: 9999px;
		font-size: 0.7rem;
		font-weight: 600;
		color: #000;
	}

	.admin-pin {
		font-size: 0.7rem;
		font-weight: 600;
	}

	.row-detail {
		padding: 1rem;
		background: var(--color-bg-secondary);
		border-top: 1px solid var(--color-border);
	}

	.detail-sections {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.detail-section h3 {
		font-size: 0.8rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		margin: 0 0 0.5rem;
	}

	.detail-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.35rem;
		font-size: 0.85rem;
	}

	.detail-grid a {
		color: var(--color-primary);
		text-decoration: none;
	}

	.chronicler-notes {
		font-size: 0.85rem;
		line-height: 1.5;
		background: var(--color-bg);
		padding: 0.75rem;
		border-radius: 4px;
		margin: 0;
	}

	.edit-form {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.form-field {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
	}

	.form-field label {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.form-field select,
	.form-field input,
	.form-field textarea {
		background: var(--color-bg);
		color: var(--color-text);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		padding: 0.4rem;
		font-size: 0.85rem;
		font-family: inherit;
	}

	.form-actions {
		display: flex;
		gap: 0.5rem;
		margin-top: 0.25rem;
	}

	.btn-save, .btn-cancel, .btn-edit {
		padding: 0.35rem 0.75rem;
		border-radius: 4px;
		border: 1px solid var(--color-border);
		font-size: 0.8rem;
		cursor: pointer;
	}

	.btn-save {
		background: var(--color-success, #22c55e);
		color: #000;
		border-color: var(--color-success, #22c55e);
	}

	.btn-cancel { background: var(--color-bg-secondary); color: var(--color-text); }
	.btn-edit { background: var(--color-bg-secondary); color: var(--color-text); }
	.btn-edit:hover { background: var(--color-bg-tertiary); }

	.admin-info {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		font-size: 0.85rem;
	}

	.admin-info .btn-edit { align-self: flex-start; margin-top: 0.5rem; }

	.muted { color: var(--color-text-muted); font-size: 0.85rem; }

	.trials {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		font-size: 0.8rem;
		font-family: monospace;
	}

	.trial {
		display: flex;
		gap: 0.5rem;
		align-items: center;
		padding: 0.3rem 0.5rem;
		border-radius: 4px;
		background: var(--color-bg);
	}

	.trial.success { border-left: 3px solid var(--color-success, #22c55e); }
	.trial.failure { border-left: 3px solid var(--color-error, #ef4444); }

	.trial-phase {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 1.2rem;
		height: 1.2rem;
		border-radius: 3px;
		background: var(--color-bg-secondary);
		font-size: 0.65rem;
		font-weight: bold;
	}

	.trial-result {
		font-weight: 600;
		min-width: 2.5rem;
	}

	.trial.success .trial-result { color: var(--color-success, #22c55e); }
	.trial.failure .trial-result { color: var(--color-error, #ef4444); }

	.trial-transport { color: var(--color-text-muted); min-width: 3rem; }
	.trial-version { color: var(--color-text-muted); min-width: 4rem; }
	.trial-date { color: var(--color-text-muted); font-size: 0.7rem; }
	.trial-tools { color: var(--color-success, #22c55e); font-size: 0.7rem; }
	.trial-error { color: var(--color-error, #ef4444); font-size: 0.7rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 300px; }
</style>
