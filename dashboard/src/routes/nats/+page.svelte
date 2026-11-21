<script lang="ts">
	import { resolve } from '$app/paths';
	import { refreshTrigger } from '#lib/stores/index.js';
	import { LoadingSpinner, Section } from '#lib/components/common/index.js';
	import { allWarnings, formatBytes, formatLimit, formatPercent, fraction, type AccountLimit, type NatsStatus } from '#lib/nats/status.js';
	import { retentionLine, storeRow } from '#lib/nats/view.js';
	import { formatCapped, type CrewStoreUsage } from '#lib/nats/crew-usage.js';
	import type { CrewRetentionStatus } from '#lib/types/kubemoot.js';

	interface CrewUsageRow {
		namespace: string;
		crew: string;
		stores: CrewStoreUsage[];
		retention: CrewRetentionStatus | null;
	}

	let status = $state<NatsStatus | null>(null);
	let crews = $state<CrewUsageRow[] | null>(null);
	let crewError = $state<string | null>(null);
	let cap = $state(0);

	async function fetchStatus() {
		try {
			const res = await fetch(resolve('/api/nats/status'));
			if (res.ok) status = await res.json();
		} catch {
			// keep the last data on screen
		}
	}

	async function fetchCrews() {
		try {
			const res = await fetch(resolve('/api/nats/crews'));
			const data = await res.json();
			if (!res.ok) throw new Error(data.error ?? `HTTP ${res.status}`);
			crews = data.crews;
			cap = data.cap;
			crewError = null;
		} catch (e) {
			crewError = e instanceof Error ? e.message : 'Failed to read crew usage';
		}
	}

	// Both read on the dashboard's refresh interval, never per keystroke.
	$effect(() => {
		$refreshTrigger;
		fetchStatus();
		fetchCrews();
	});

	const rows = $derived((status?.stores ?? []).map(storeRow));
	const warnings = $derived(allWarnings(status?.stores ?? []));

	function account(a: AccountLimit): string {
		const share = formatPercent(fraction(a.used, a.limit));
		const text = `${formatBytes(a.used)} of ${formatLimit(a.limit, formatBytes)}`;
		return share === '' ? text : `${text} (${share})`;
	}

	function count(a: AccountLimit): string {
		return `${a.used} of ${formatLimit(a.limit)}`;
	}
</script>

<div class="nats-page">
	<header class="header">
		<h1>NATS</h1>
		<p class="subtitle">
			Everything Kubemoot keeps lives in NATS JetStream, on your cluster. This page reads it; it changes nothing.
			Retention and limits are described in <a href="https://kubemoot.org/docs/operating/data-ownership/" target="_blank" rel="noopener noreferrer">Data ownership</a>.
		</p>
	</header>

	{#if !status}
		<div class="loading" role="status" aria-label="Loading NATS"><LoadingSpinner size="sm" /></div>
	{:else if !status.connected}
		<div class="error-banner">NATS is not reachable: {status.error}</div>
	{:else}
		{#if warnings.length > 0}
			<div class="warning-band" data-testid="nats-warnings">
				{#each warnings as w (w.store + w.kind)}
					<div class={w.level}>{w.store} is at {formatPercent(w.fraction)} of its {w.kind} limit</div>
				{/each}
			</div>
		{/if}

		<Section title="Server and JetStream account">
			<dl class="facts">
				<dt>Server</dt><dd>{status.server?.version} {status.server?.name}</dd>
				{#if status.server?.uptime}<dt>Uptime</dt><dd>{status.server.uptime}</dd>{/if}
				{#if status.server?.connections !== null && status.server?.connections !== undefined}<dt>Connections</dt><dd>{status.server.connections}</dd>{/if}
				{#if status.account}
					<dt>File storage</dt><dd>{account(status.account.storage)}</dd>
					<dt>Memory</dt><dd>{account(status.account.memory)}</dd>
					<dt>Streams</dt><dd>{count(status.account.streams)}</dd>
					<dt>Consumers</dt><dd>{count(status.account.consumers)}</dd>
				{/if}
				{#if status.historyReads}
					<dt>Discussion history reads</dt>
					<dd>
						last {status.historyReads.lastMs} ms, {status.historyReads.lastMessages} messages, {formatBytes(status.historyReads.lastBytes)};
						average {status.historyReads.avgMs} ms, slowest {status.historyReads.maxMs} ms over {status.historyReads.count} reads
					</dd>
				{/if}
			</dl>
		</Section>

		<Section title="Streams, KV buckets and object stores">
			<p class="note" data-testid="nats-share-note">
				{#if status.scoped}
					This dashboard serves some namespaces only: messages and bytes are their share of each store (bytes are an estimate), and sequences and consumers are not shown.
				{:else}
					Messages, bytes, sequences and consumers are cluster-wide totals for each store.
				{/if}
			</p>
			<div class="table-wrap">
				<table data-testid="nats-stores">
					<thead>
						<tr>
							<th>Store</th><th>Owner</th><th>People's data</th><th>Messages</th><th>Bytes</th>
							<th>Max age</th><th>Max bytes</th><th>Max messages</th><th>KV history</th>
							<th>Sequences</th><th>Consumers</th><th>Crew policy</th>
						</tr>
					</thead>
					<tbody>
						{#each rows as r (r.name)}
							<tr class:people={r.holdsPeoplesData}>
								<td><strong>{r.name}</strong><div class="kind">{r.kind}</div><div class="holds">{r.holds}</div></td>
								<td>{r.owner}</td>
								<td>{r.peoplesData}</td>
								<td class="num">{r.messages}</td>
								<td class="num">{r.bytes}{#if r.bytesShare}<div class="kind">{r.bytesShare} of limit</div>{/if}</td>
								<td>{r.maxAge}</td>
								<td>{r.maxBytes}</td>
								<td>{r.maxMessages}</td>
								<td>{r.history}</td>
								<td>{r.sequences}</td>
								<td class="num">{r.consumers}</td>
								<td>{#if r.crewPolicy}<code>{r.crewPolicy}</code>{/if}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
			{#if status.absent.length > 0}
				<p class="note">Not created on this install: {status.absent.join(', ')}.</p>
			{/if}
		</Section>

		<Section title="Per crew">
			<p class="note">
				Each crew's share of the stores keyed by crew (<code>&lt;namespace&gt;.&lt;crew&gt;</code>). Message and key counts come from NATS;
				bytes in a stream or KV bucket are the crew's share of the messages applied to the store's bytes, so they are an estimate. Object store sizes are exact.
				At most {cap.toLocaleString('en-US')} entries are counted per crew and store.
			</p>
			{#if crewError}
				<div class="error-banner">{crewError}</div>
			{:else if !crews}
				<div class="loading" role="status" aria-label="Loading crew usage"><LoadingSpinner size="sm" /></div>
			{:else if crews.length === 0}
				<p class="note">No crews.</p>
			{:else}
				{#each crews as c (c.namespace + '/' + c.crew)}
					<div class="crew" data-testid="nats-crew">
						<h3>{c.namespace}/{c.crew}</h3>
						<p class="note">Last retention cleanup: {retentionLine(c.retention)}</p>
						<table>
							<thead><tr><th>Store</th><th>Messages</th><th>Keys</th><th>Bytes</th></tr></thead>
							<tbody>
								{#each c.stores as u (u.store)}
									<tr>
										<td>{u.store}</td>
										<td class="num">{formatCapped(u.messages, u.capped)}</td>
										<td class="num">{u.keys.toLocaleString('en-US')}</td>
										<td class="num">{u.bytesEstimated ? 'about ' : ''}{formatBytes(u.bytes)}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/each}
			{/if}
		</Section>
	{/if}
</div>

<style>
	.nats-page {
		max-width: 1400px;
	}
	.header h1 {
		font-size: 1.75rem;
		font-weight: 600;
	}
	.subtitle,
	.note,
	.kind,
	.holds {
		color: var(--color-text-muted);
		font-size: 0.8rem;
	}
	.holds {
		max-width: 28rem;
	}
	.error-banner,
	.warning-band {
		border-radius: 0.5rem;
		padding: 0.75rem 1rem;
		margin: 1rem 0;
	}
	.error-banner {
		background: rgba(239, 68, 68, 0.15);
		border: 1px solid var(--color-error);
		color: var(--color-error);
	}
	.warning-band {
		background: rgba(245, 158, 11, 0.12);
		border: 1px solid #f59e0b;
		font-size: 0.85rem;
	}
	.warning-band .critical {
		color: var(--color-error);
		font-weight: 600;
	}
	.facts {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 0.3rem 1rem;
		margin: 0;
		font-size: 0.85rem;
	}
	.facts dt {
		color: var(--color-text-muted);
	}
	.facts dd {
		margin: 0;
	}
	.table-wrap {
		overflow-x: auto;
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.8rem;
	}
	th,
	td {
		text-align: left;
		padding: 0.4rem 0.6rem;
		border-bottom: 1px solid var(--color-border);
		vertical-align: top;
	}
	td.num {
		text-align: right;
		font-variant-numeric: tabular-nums;
	}
	tr.people td:first-child {
		border-left: 3px solid #f59e0b;
	}
	.crew h3 {
		font-size: 0.95rem;
		margin: 1rem 0 0.25rem;
	}
	.loading {
		min-height: 2.5rem;
		display: flex;
		align-items: center;
	}
</style>
