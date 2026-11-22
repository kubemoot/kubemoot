<script lang="ts">
	import { errorOnFailure, loadingOnStart } from '#lib/client/refresh-state.js';
	import { reportComparator } from '#lib/mcp-report-sort.js';
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { refreshTrigger } from '#lib/stores/index.js';
	import { ResourceList } from '#lib/components/layout/index.js';
	import { InfoPopover } from '#lib/components/common/index.js';
	import type { MCPServerReport } from '#lib/types/kubemoot.js';

	let reports = $state<MCPServerReport[]>([]);
	let loaded = false;
	let loading = $state(true);
	let error = $state<string | null>(null);
	let expandedReport = $state<string | null>(null);
	let filterVerdict = $state('all');
	let sortBy = $state('lastTested');

	async function fetchReports() {
		loading = loadingOnStart(loaded);
		error = null;
		try {
			const res = await fetch(resolve('/api/kubemoot/mcpserverreports'));
			const data = await res.json();
			if (data.error) throw new Error(data.error);
			reports = data.items || [];
		} catch (e) {
			error = errorOnFailure(loaded, e, 'Failed to fetch reports');
		} finally {
			loaded = loaded || error === null;
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
	route="/mcpreports"
	count={filteredReports.length}
	{loading}
	{error}
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
					Verdict <InfoPopover label="About Verdict" lines={["use = reliable, passed all tests. caution = some failures, use carefully. avoid = mostly failing, not recommended. untested = no test data."]} />
				</span>
				<span class="col-rate">Success%</span>
				<span class="col-transport">
					Transport <InfoPopover label="About Transport" lines={["stdio = standard I/O via MCP Bridge sidecar (recommended for most servers). http = direct HTTP/SSE connection."]} />
				</span>
				<span class="col-tested">Last Tested</span>
				<span class="col-admin">
					Admin <InfoPopover label="About Admin" lines={["auto = verdict computed from trial history. pinned = admin manually set the verdict override."]} />
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
									<div class="admin-info">
										{#if report.spec?.adminVerdict}
											<div><strong>Pinned:</strong> {report.spec.adminVerdict} by {report.spec.adminAuthor || 'unknown'}</div>
											<div><strong>Reason:</strong> {report.spec.adminNotes || '-'}</div>
										{:else}
											<div class="muted">No admin override. Verdict is auto-computed from trial history.</div>
										{/if}
									</div>
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
		overflow-x: auto;
		width: 100%;
	}

	.table-header {
		display: grid;
		min-width: 710px;
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
		min-width: 710px;
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
		color: var(--color-on-warning);
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

	.admin-info {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		font-size: 0.85rem;
	}


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
