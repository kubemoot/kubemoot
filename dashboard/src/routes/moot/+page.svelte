<script lang="ts">
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { marked } from 'marked';
	import { threadStartedAt } from '#lib/stores/discussions.js';
	import { crewDirectory, forgetThreads, threads, sortedThreads, discussionsConnected, historyLoaded, initDiscussions, ensureThreadLoaded, loadOlder, loadingOlder, hasOlder, showStandAsides, showDebugDetail, namespace } from '#lib/stores/index.js';
	import type { Thread } from '#lib/stores/index.js';
	import { tryCrewScope, type CrewScope } from '#lib/crewScope.js';
	import { crewEntry, crewLabel, crewPillNamespace, crewTooltip } from '#lib/crew-display-name.js';
	import { AnswerPanel, DiscussionMenu, DiscussionSpanGraph, MessageStatsLine, PlacementPanel } from '#lib/components/discussions/index.js';
	import { visibleMessages, afterAnswerMessages } from '#lib/conversation-view.js';
	import { placementCallsOf } from '#lib/placement-panel.js';
	import { formatTimestamp, messageStats, summarizeThread } from '#lib/discussion-timing.js';
	import { formatElapsed } from '#lib/duration-format.js';
	import { SYNTHESIS_COLLAPSE_CHARS, artifactKey, artifactHref } from '#lib/discussion-artifacts.js';
	import { aggregateAgents, renderAgentSummary } from '#lib/discussion-agent-summary.js';
	import { threadStartMs as messagesStartMs } from '#lib/discussion-spans.js';
	import { buildAcceleratorDisplayMap } from '#lib/accelerator-display.js';
	import { answerState } from '#lib/conversation-answer.js';
	import { statusIcon } from '#lib/thread-status-icon.js';
	import { browserTimeZone, exportHref, threadConversationId } from '#lib/conversation-export.js';
	import { FilterBar, ThreadDeleteDialog } from '#lib/components/conversations/index.js';
	import { HoverActions } from '#lib/components/common/index.js';
	import { copyText } from '#lib/clipboard.js';
	import type { HoverAction } from '#lib/hover-actions.js';
	import { rowActions } from '#lib/row-actions.js';
	import { ListCard, ListDetailLayout } from '#lib/components/layout/index.js';
	import { listMine } from '#lib/ask-client.js';
	import { mineScopesFor } from '#lib/list-scope.js';
	import { emptyBecauseOfFilter, matchesFilter } from '#lib/conversation-filter.js';
	import { createListFilter } from '#lib/list-filter.svelte.js';
	import { fetchDashboardVersion, versionLabel, VERSION_UNKNOWN } from '#lib/dashboard-version.js';

	// Configure marked for safe inline rendering
	marked.setOptions({ breaks: true, gfm: true });

	interface ChannelInfo {
		name: string;
		color: string;
		agentCount: number;
	}

	/** The channel every thread uses unless a crew defines more; its name is not worth showing. */
	const DEFAULT_CHANNEL = 'general';

	let selectedThreadId = $state<string | null>(null);

	// $crewDirectory holds the crews (namespace, crew, display name) of the global
	// top-bar "Crew:" selector, loaded by the root layout. The
	// `namespace` store holds the selected crew NAMESPACE ('' = All crews). Threads
	// carry the namespace from their subject, so the list filters by namespace and
	// same-named crews in other namespaces stay out of view.
	const selectedScope = $derived<CrewScope | null>(
		$namespace === ''
			? null
			: tryCrewScope($namespace, crewEntry($crewDirectory, $namespace)?.crew)
	);
	const selectedCrew = $derived(selectedScope?.crew ?? null);

	// The filters and the search are the Conversations page's, in the URL like its own, so a link
	// reproduces the list; the open thread stays in ?thread=.
	// The filters and the search live in the URL query, so a link reproduces the view; a link with
	// none opens on the filter this viewer last chose.
	const listFilter = createListFilter('moot', '/moot');
	onMount(() => listFilter.restore());
	const filter = $derived(listFilter.filter);
	const setFilter = listFilter.set;
	const signedIn = $derived(!!page.data.user);

	// The conversations the signed-in person started, for the Mine filter.
	let mine = $state<Set<string> | null>(null);
	let mineError = $state('');
	const mineScopes = $derived(mineScopesFor(selectedScope, $crewDirectory));
	$effect(() => {
		const scopes = mineScopes;
		mine = null;
		mineError = '';
		if (!signedIn || !filter.mine || scopes.length === 0) return;
		void listMine(fetch, scopes).then((found) => {
			mine = found.ids;
			mineError = found.error;
		});
	});

	const visibleThreads = $derived(
		($namespace === '' ? $sortedThreads : $sortedThreads.filter((t) => t.namespace === $namespace)).filter((t) =>
			matchesFilter(t, filter, mine)
		)
	);

	// Per-synthesis expand toggle (keyed by thread:messageId): a long synthesis
	// collapses to a faded preview by default and expands to the full text on demand.
	// Same pattern + helpers as the fitness transcript view (#lib/discussion-artifacts).
	let openSynthesis = $state<Record<string, boolean>>({});

	// Thread actions state
	let showSpanGraph = $state(false);
	let dashboardVersion = $state(VERSION_UNKNOWN);
	// Server-id -> discovered accelerator model (e.g. "ollama-rig1" -> "RTX 4090"), so the
	// span graph shows the human accelerator name instead of the raw server id. Sourced
	// from the operator's DCGM discovery (ModelServer.status.capacity.acceleratorModel);
	// no accelerator model is hardcoded.
	let acceleratorDisplayMap = $state<Record<string, string>>({});

	// Dynamic channels from API
	let channels = $state<ChannelInfo[]>([{ name: 'general', color: '#6b7280', agentCount: 0 }]);
	let channelColors = $derived(
		Object.fromEntries(channels.map((ch) => [ch.name, ch.color]))
	);

	const messageTypeLabels: Record<string, string> = {
		thread_start: 'Started',
		advisory: 'Advisory',
		advisory_ready: 'Evaluate',
		subcommittee: 'Subcommittee',
		review_ready: 'Review',
		agree: 'Agree',
		concern: 'Concern',
		block: 'Block',
		stand_aside: 'Stand Aside',
		failure: 'Failure',
		proposal: 'Proposal',
		consent: 'Consent',
		synthesis: 'Synthesis',
		follow_up: 'Follow-up',
		reply: 'Reply',
		thread_close: 'Closed',
		thread_pause: 'Paused',
		thread_resume: 'Resumed',
		stop_requested: 'Stop',
		triage_result: 'Triage',
		gap_detected: 'Gap Detected',
		waking: 'Waking',
		ready: 'Ready',
		// Legacy
		contribution: 'Agree',
		decline: 'Stand Aside'
	};

	const selectedThread = $derived(selectedThreadId ? $threads.get(selectedThreadId) : null);
	// Per-message elapsed time, step durations, tokens and placement, and the thread's
	// summary, derived from the selected thread's messages as they arrive.
	const selectedStats = $derived(messageStats(selectedThread?.messages ?? []));
	const selectedPlacementCalls = $derived(placementCallsOf(selectedThread?.messages ?? []));
	const selectedSummary = $derived(
		selectedThread ? summarizeThread(selectedThread.messages, selectedStats) : null
	);

	const timelineMessages = $derived(
		visibleMessages(selectedThread?.messages ?? [], $showDebugDetail).filter(
			(m) => $showStandAsides || m.messageType !== 'stand_aside'
		)
	);
	const selectedConversationId = $derived(selectedThread ? threadConversationId(selectedThread) : undefined);
	const selectedThreadScope = $derived(selectedThread ? tryCrewScope(selectedThread.namespace, selectedThread.crew) : null);
	const selectedAnswer = $derived(answerState(selectedThread?.messages ?? [], selectedThread?.status ?? 'open'));
	const answerElapsed = $derived.by(() => {
		if (selectedAnswer.kind !== 'answered') return '';
		const m = selectedThread?.messages.find((x) => x.messageId === selectedAnswer.messageId);
		return (m && formatElapsed(selectedStats.get(m)?.elapsedMs)) || '';
	});
	const selectedStatus = $derived(statusIcon(selectedAnswer.kind, selectedThread ?? undefined));
	const afterAnswer = $derived(afterAnswerMessages(selectedThread?.messages ?? []));

	/** An agent's name as the timeline shows it. */
	function agentLabel(agent: string): string {
		return agent === 'human' ? 'You' : agent.replace('homelab-', '');
	}

	// Maps a raw thread status to its human label. Shared by the thread-list row
	// and the detail pane so both stay in lockstep (open->Active, synthesized->Answered).
	function statusLabel(status: string): string {
		if (status === 'open') return 'Active';
		if (status === 'synthesized') return 'Answered';
		return 'Closed';
	}

	// Opening a thread reads its own subject, so a thread that began before the loaded
	// history shows complete without loading the stream around it.
	$effect(() => {
		if (selectedThreadId) void ensureThreadLoaded(selectedThreadId);
	});

	// Older threads load a page at a time when the list is scrolled near its end.
	function onListScroll(e: Event) {
		const el = e.currentTarget as HTMLElement;
		if (el.scrollHeight - el.scrollTop - el.clientHeight < 200) void loadOlder();
	}

	function selectThread(threadId: string) {
		selectedThreadId = threadId;
	}

	function buildThreadHeader(thread: Thread, version: string): string {
		let text = `# ${thread.userQuery}\n`;
		text += `**Discussion**: ${thread.threadId}\n`;
		text += `**Kubemoot Dashboard**: ${versionLabel(version)}\n`;
		if (thread.namespace) text += `**Namespace**: ${thread.namespace}\n`;
		if (thread.crew) text += `**Crew**: ${thread.crew}\n`;
		if (thread.crewVersion) text += `**Crew version**: ${thread.crewVersion}\n`;
		text += `Channel: ${thread.channel} | Status: ${thread.status} | Started: ${thread.startedAt}\n\n`;
		return text;
	}

	function renderMessageTimeline(thread: Thread): string {
		let text = `## Messages\n`;
		const stats = messageStats(thread.messages);
		for (const msg of thread.messages) {
			const agent = agentLabel(msg.agentName);
			const elapsed = formatElapsed(stats.get(msg)?.elapsedMs);
			const when = elapsed ? ' (' + elapsed + ')' : '';
			text += `**${agent}** [${messageTypeLabels[msg.messageType] || msg.messageType}]${when}\n`;
			if (msg.content) text += `${msg.content}\n`;
			text += '\n';
		}
		return text;
	}

	async function formatThreadForClipboard(thread: Thread): Promise<string> {
		const version = await fetchDashboardVersion(fetch, resolve('/api/version'));
		const t0 = messagesStartMs(thread.messages);
		const agentMap = aggregateAgents(thread.messages, t0);
		return buildThreadHeader(thread, version) + renderAgentSummary(agentMap, agentLabel) + renderMessageTimeline(thread);
	}

	async function copyThread() {
		if (!selectedThread) return;
		const text = await formatThreadForClipboard(selectedThread);
		await navigator.clipboard.writeText(text);
	}

	// Build server-id -> accelerator model map from the operator-discovered ModelServer
	// status. Keys on BOTH the CR name and the endpoint namespace, because span
	// metadata may carry either form (AcceleratorLabels.fromServer vs fromEndpoint).
	async function loadAcceleratorLabels() {
		try {
			const res = await fetch(`${resolve('/api/kubemoot/modelservers')}?namespace=`);
			const data = await res.json();
			const items = Array.isArray(data?.items) ? data.items : [];
			acceleratorDisplayMap = buildAcceleratorDisplayMap(items);
		} catch (e) {
			console.error('Failed to load accelerator labels:', e);
		}
	}

	async function loadChannels() {
		try {
			const res = await fetch(resolve('/api/kubemoot/channels'));
			const data = await res.json();
			if (data.channels && data.channels.length > 0) {
				channels = data.channels;
			}
		} catch (e) {
			console.error('Failed to load channels:', e);
		}
	}

	// The icon bars: Copy on every message, and Copy id and Delete on every list row.
	function messageActions(content: string): HoverAction[] {
		return [{ label: 'Copy message', id: 'copy-message', icon: 'copy', run: () => copyText(content) }];
	}

	let deletingRow = $state<Thread | null>(null);

	const rowActionsOf = (thread: Thread) => rowActions(thread, thread.userQuery, { canDelete: signedIn, ondelete: () => { deletingRow = thread; } });

	let detailEl = $state<HTMLElement | null>(null);
	let copiedThreadId = $state(false);

	async function copyThreadId() {
		if (!selectedThread) return;
		const text = `Thread id: ${selectedThread.threadId} | ${selectedThread.userQuery}`;
		await navigator.clipboard.writeText(text);
		copiedThreadId = true;
		setTimeout(() => { copiedThreadId = false; }, 2000);
	}

	// An open thread that gains messages follows them when the reader is already at the
	// bottom of the detail pane; a finished thread opens at its top.
	$effect(() => {
		// Reading the message count registers the reactive dependency, so this
		// effect re-runs as new messages arrive.
		const count = selectedThread?.messages.length ?? 0;
		const follow = selectedThread?.status === 'open';
		if (!detailEl || count === 0 || !follow) return;
		const el = detailEl;
		const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 120;
		if (atBottom) requestAnimationFrame(() => { el.scrollTop = el.scrollHeight; });
	});

	// Keep the selection consistent with the crew filter: if the selected thread
	// is filtered out, fall back to the first visible thread (or none). Guarded so
	// it only writes when the value actually changes - no infinite loop.
	$effect(() => {
		if (selectedThreadId && !visibleThreads.some((t) => t.threadId === selectedThreadId)) {
			selectedThreadId = visibleThreads[0]?.threadId ?? null;
		}
	});

	onMount(() => {
		initDiscussions();
		loadChannels();
		loadAcceleratorLabels();
		fetchDashboardVersion(fetch, resolve('/api/version')).then((v) => { dashboardVersion = v; });
		// A link from Conversations opens its thread: /moot?thread=<id>
		selectedThreadId = new URLSearchParams(location.search).get('thread') ?? selectedThreadId;
		// Auto-select first thread once history loads (if nothing selected)
		const unsub = sortedThreads.subscribe((sorted) => {
			if (sorted.length > 0 && !selectedThreadId) {
				selectedThreadId = sorted[0].threadId;
			}
		});
		return () => {
			unsub();
		};
	});
</script>

<ListDetailLayout route="/moot" onlistscroll={onListScroll} bind:detailEl detailOpen={!!selectedThread}>
	{#snippet actions()}
		<div class="connection-status" class:connected={$discussionsConnected}>
			<span class="status-dot"></span>
			{$discussionsConnected ? 'Connected' : 'Disconnected'}
		</div>
	{/snippet}
	{#snippet list()}
			<FilterBar {filter} {signedIn} loaded={$sortedThreads.length} hasOlder={$hasOlder} onchange={setFilter} onloadolder={() => void loadOlder()} error={mineError} />
			{#if !$historyLoaded && $sortedThreads.length === 0}
				<div class="empty-state">Loading discussion history...</div>
			{:else if visibleThreads.length === 0}
				<div class="empty-state">
					{#if emptyBecauseOfFilter(filter, 'discussions')}
						{emptyBecauseOfFilter(filter, 'discussions')}
					{:else if selectedCrew}
						No discussions for crew "{crewLabel($crewDirectory, $namespace, selectedCrew)}". Pick "All crews" to see all.
					{:else}
						No discussions yet.
					{/if}
				</div>
			{:else}
				{#each visibleThreads as thread (thread.threadId)}
					<ListCard selected={selectedThreadId === thread.threadId} onopen={() => selectThread(thread.threadId)} actions={rowActionsOf(thread)}>
						{#snippet head()}
							{#if thread.crew && $namespace === ''}
								<span class="crew-badge" title={crewTooltip($crewDirectory, thread.namespace, thread.crew)}>{crewLabel($crewDirectory, thread.namespace, thread.crew)}{#if crewPillNamespace($crewDirectory, thread.namespace, thread.crew)}<span class="pill-ns">{thread.namespace}</span>{/if}</span>
							{/if}
							{#if thread.channel !== DEFAULT_CHANNEL}
								<span class="channel-badge" style="background-color: {channelColors[thread.channel] || '#6b7280'}">{thread.channel}</span>
							{/if}
							<span class="thread-status" class:open={thread.status === 'open'}
								class:synthesized={thread.status === 'synthesized'}
								class:closed={thread.status === 'closed'}>
								{statusLabel(thread.status)}
							</span>
						{/snippet}
						{thread.userQuery}
						{#snippet foot()}
							<span>
								{thread.agreeCount} agree{#if thread.concernCount > 0}, {thread.concernCount} concern{thread.concernCount !== 1 ? 's' : ''}{/if}{#if thread.blockCount > 0}, {thread.blockCount} block{thread.blockCount !== 1 ? 's' : ''}{/if}{#if thread.standAsideCount > 0}, {thread.standAsideCount} aside{/if}
							</span>
							<span title={threadStartedAt(thread)}>{formatTimestamp(threadStartedAt(thread))}</span>
						{/snippet}
					</ListCard>
				{/each}
				{#if $hasOlder}
					<button class="load-older" disabled={$loadingOlder} onclick={() => loadOlder()}>
						{$loadingOlder ? 'Loading older threads...' : 'Load older threads'}
					</button>
				{/if}
			{/if}
	{/snippet}
	{#snippet detail()}
			{#if selectedThread}
				<div class="thread-detail-header">
					{#if selectedThread.crew && $namespace === ''}
						<span class="crew-badge" title={crewTooltip($crewDirectory, selectedThread.namespace, selectedThread.crew)}>{crewLabel($crewDirectory, selectedThread.namespace, selectedThread.crew)}{#if crewPillNamespace($crewDirectory, selectedThread.namespace, selectedThread.crew)}<span class="pill-ns">{selectedThread.namespace}</span>{/if}</span>
					{/if}
					{#if selectedThread.channel && selectedThread.channel !== DEFAULT_CHANNEL}
						<span class="channel-badge large" style="background-color: {channelColors[selectedThread.channel] || '#6b7280'}">{selectedThread.channel}</span>
					{/if}
					<div class="thread-query-full" title={selectedThread.threadId}>
						<span>{selectedThread.userQuery}</span>
						<button use:tooltip={ACTIONS.copyThreadId} class="thread-id-copy" onclick={copyThreadId} aria-label="Copy thread id with the question">
							<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
								{#if copiedThreadId}<polyline points="20 6 9 17 4 12" />{:else}<rect x="9" y="9" width="13" height="13" rx="2" /><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />{/if}
							</svg>
						</button>
					</div>
					<div class="thread-actions">
						<span class="status-icon {selectedStatus.kind}" role="img" title={selectedStatus.tooltip} aria-label={selectedStatus.label}>
							<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
								{#if selectedStatus.kind === 'working'}
									<circle cx="12" cy="12" r="5" fill="currentColor" />
								{:else if selectedStatus.kind === 'answered'}
									<circle cx="12" cy="12" r="9" /><polyline points="8 12.5 11 15.5 16 9" />
								{:else if selectedStatus.kind === 'failed'}
									<circle cx="12" cy="12" r="9" /><line x1="9" y1="9" x2="15" y2="15" /><line x1="15" y1="9" x2="9" y2="15" />
								{:else}
									<circle cx="12" cy="12" r="9" /><rect x="9" y="9" width="6" height="6" fill="currentColor" />
								{/if}
							</svg>
						</span>
						<DiscussionMenu
							showTimeline={showSpanGraph}
							showDebug={$showDebugDetail}
							ontoggletimeline={() => { showSpanGraph = !showSpanGraph; }}
							ontoggledebug={() => showDebugDetail.toggle()}
							oncopydiscussion={copyThread}
							oncopythreadid={copyThreadId}
							downloadHref={selectedConversationId ? exportHref(resolve('/api/kubemoot/conversation-export'), selectedThreadScope, [selectedConversationId], browserTimeZone()) : null}
						/>
					</div>
				</div>
				{#if selectedSummary?.startedAt && formatTimestamp(selectedSummary.startedAt)}
					<div class="thread-started" title={selectedSummary.startedAt}>
						Started {formatTimestamp(selectedSummary.startedAt)}; times below are since then
					</div>
				{/if}

				<AnswerPanel state={selectedAnswer} elapsed={answerElapsed} summary={selectedSummary} {agentLabel} />

				{#if showSpanGraph && selectedThread}
					<div class="span-graph-panel">
						<DiscussionSpanGraph
							messages={selectedThread.messages}
							threadId={selectedThread.threadId}
							userQuery={selectedThread.userQuery}
							version={dashboardVersion}
							crew={selectedThread.crew}
							{acceleratorDisplayMap}
						/>
					</div>
				{/if}

				<div class="message-timeline">
					{#each timelineMessages as msg, msgIdx}
						{@const stats = selectedStats.get(msg) ?? {}}
						<div
							class="message-card has-hover-actions {msg.messageType}"
							class:synthesis={msg.messageType === 'synthesis'}
							class:decline={msg.messageType === 'stand_aside'}
							class:advisory={msg.messageType === 'advisory'}
							class:advisory_ready={msg.messageType === 'advisory_ready'}
							class:review_ready={msg.messageType === 'review_ready'}
							class:concern={msg.messageType === 'concern'}
							class:block={msg.messageType === 'block'}
							class:proposal={msg.messageType === 'proposal'}
							class:human={msg.agentName === 'human'}
							class:after-answer={afterAnswer.has(msg)}
							id="message-{msg.messageId}"
							tabindex="-1"
						>
							{#if msg.content}<HoverActions actions={messageActions(msg.content)} label="Message actions" />{/if}
							<div class="message-header">
								<span class="agent-name" class:human-name={msg.agentName === 'human'}>
									{agentLabel(msg.agentName)}
								</span>
								<span class="message-type-badge {msg.messageType}">
									{messageTypeLabels[msg.messageType] || msg.messageType}
								</span>
								{#if msg.messageType === 'stand_aside' && msg.metadata?.reason}
									<span class="decline-reason">({msg.metadata.reason})</span>
								{/if}
								{#if afterAnswer.has(msg)}
									<span class="after-answer-note" title="Sent after the answer was delivered">after the answer</span>
								{/if}
								{#if $showDebugDetail}
									<span class="message-time" title={formatTimestamp(msg.timestamp)}>{formatElapsed(stats.elapsedMs)}</span>
								{/if}
							</div>
							{#if (msg.messageType === 'advisory' || msg.messageType === 'advisory_ready') && msg.metadata?.technologies && msg.metadata.technologies.length > 0}
								<div class="tech-badges">
									{#each msg.metadata.technologies as tech}
										<span class="tech-badge">{tech}</span>
									{/each}
								</div>
							{/if}
							{#if msg.content}
								{@const skey = `${msgIdx}`}
								{@const isSynthesis = msg.messageType === 'synthesis'}
								{@const synthLong = isSynthesis && msg.content.length > SYNTHESIS_COLLAPSE_CHARS}
								{@const aKey = artifactKey(msg.content)}
								<div class="message-content markdown synth-body" class:collapsed={synthLong && !openSynthesis[skey]}>{@html marked.parse(msg.content)}</div>
								{#if synthLong}<button class="finding-toggle" onclick={() => (openSynthesis[skey] = !openSynthesis[skey])}>{openSynthesis[skey] ? 'show less' : '… more'}</button>{/if}
								{#if aKey}<a class="artifact-dl" href={artifactHref(aKey)} download>download</a>{/if}
							{/if}
							{#if msg.metadata?.toolsUsed && msg.metadata.toolsUsed.length > 0}
								<div class="tools-used">
									{#each msg.metadata.toolsUsed as tool}
										<span class="tool-badge">{tool}</span>
									{/each}
								</div>
							{/if}
							{#if msg.messageType === 'triage_result' && msg.metadata?.agents}
								<div class="triage-agents">
									{#each msg.metadata.agents as agent}
										<span class="triage-agent-badge" title={agent.reason}>
											{agent.name.replace('homelab-', '')}
											<span class="triage-confidence">{(agent.confidence * 100).toFixed(0)}%</span>
										</span>
									{/each}
									{#if msg.metadata.overallConfidence != null}
										<span class="triage-overall">Overall: {(msg.metadata.overallConfidence * 100).toFixed(0)}%</span>
									{/if}
								</div>
							{/if}
							{#if $showDebugDetail}
								<MessageStatsLine {stats} acceleratorNames={acceleratorDisplayMap} />
							{/if}
						</div>
					{/each}
					{#if $showDebugDetail && selectedPlacementCalls.length > 0}
						<PlacementPanel calls={selectedPlacementCalls} {agentLabel} />
					{/if}
				</div>

				<!-- Room for the follow-up input at the bottom of an open discussion. -->
				<div class="reply-bar"></div>
			{:else}
				<div class="no-selection">
					Select a discussion thread to view its timeline
				</div>
			{/if}
	{/snippet}
</ListDetailLayout>

{#if deletingRow}
	{@const target = deletingRow}
	<ThreadDeleteDialog
		thread={target}
		title={target.userQuery}
		onclose={() => (deletingRow = null)}
		ondeleted={(report) => {
			deletingRow = null;
			forgetThreads([...(report.threads ?? []), target.threadId]);
		}}
	/>
{/if}

<style>



	.connection-status {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.status-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background-color: #ef4444;
	}

	.connection-status.connected .status-dot {
		background-color: #10b981;
	}



	.load-older {
		background: transparent;
		border: 1px dashed var(--color-border);
		border-radius: 0.5rem;
		color: var(--color-text-muted);
		padding: 0.5rem;
		font-size: 0.8rem;
		cursor: pointer;
	}

	.empty-state {
		padding: 2rem 1rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}





	.crew-badge {
		display: inline-block;
		padding: 0.125rem 0.5rem;
		border-radius: 1rem;
		font-size: 0.65rem;
		font-weight: 600;
		background: rgba(168, 85, 247, 0.15);
		color: var(--color-purple);
		font-family: var(--font-mono);
	}

	.pill-ns { margin-left: 0.35rem; font-weight: 400; opacity: 0.6; }

	.channel-badge {
		display: inline-block;
		padding: 0.125rem 0.5rem;
		border-radius: 1rem;
		font-size: 0.65rem;
		font-weight: 600;
		text-transform: uppercase;
		color: white;
	}

	.channel-badge.large {
		font-size: 0.75rem;
		padding: 0.25rem 0.75rem;
	}

	.thread-status {
		margin-left: auto;
		font-size: 0.65rem;
		font-weight: 500;
		text-transform: uppercase;
	}

	.thread-status.open { color: var(--accent-green); }
	.thread-status.synthesized { color: var(--accent-amber); }
	.thread-status.closed { color: var(--color-text-muted); }




	.thread-detail-header {
		display: flex;
		align-items: flex-start;
		flex-wrap: wrap;
		gap: 0.5rem 1rem;
	}

	.thread-detail-header .thread-query-full {
		flex: 1 1 14rem;
		min-width: 0;
	}

	.thread-detail-header .crew-badge, .thread-detail-header .channel-badge {
		align-self: center;
	}

	.human-badge {
		font-size: 0.65rem;
		padding: 0.125rem 0.5rem;
		border-radius: 0.25rem;
		background: rgba(16, 185, 129, 0.2);
		color: var(--accent-green);
		font-weight: 500;
	}

	.message-timeline {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		/* The detail pane scrolls, not the list: a list that shared the pane's height with
		   the span graph shrank to nothing. */
		flex: 0 0 auto;
	}

	.message-card {
		position: relative;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 0.75rem;
	}

	.message-card.synthesis {
		border-color: #f59e0b;
		background: rgba(245, 158, 11, 0.05);
	}

	.message-card.advisory {
		border-color: #a855f7;
		background: rgba(168, 85, 247, 0.05);
	}

	.message-card.advisory_ready {
		border-color: #0ea5e9;
		background: rgba(14, 165, 233, 0.05);
	}

	.message-card.review_ready {
		border-color: #f59e0b;
		background: rgba(245, 158, 11, 0.05);
	}

	.message-card.concern {
		border-color: #f59e0b;
		background: rgba(245, 158, 11, 0.05);
	}

	.message-card.block {
		border-color: #ef4444;
		background: rgba(239, 68, 68, 0.08);
	}

	.message-card.proposal {
		border-color: #3b82f6;
		background: rgba(59, 130, 246, 0.05);
	}

	.message-card.decline {
		opacity: 0.5;
		border-color: var(--color-border);
		background: transparent;
	}

	.message-card.human {
		border-color: #10b981;
		background: rgba(16, 185, 129, 0.05);
	}

	.decline-reason {
		font-size: 0.7rem;
		color: var(--color-text-muted);
		font-style: italic;
	}

	.message-header {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 0.5rem;
	}

	.agent-name {
		font-weight: 600;
		font-size: 0.8rem;
		color: var(--color-text);
	}

	.human-name {
		color: var(--accent-green);
	}

	.message-type-badge {
		font-size: 0.6rem;
		font-weight: 600;
		text-transform: uppercase;
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
	}

	.message-type-badge.thread_start { background: rgba(59, 130, 246, 0.2); color: var(--accent-blue); }
	.message-type-badge.advisory { background: rgba(168, 85, 247, 0.2); color: var(--accent-purple); }
	.message-type-badge.advisory_ready { background: rgba(14, 165, 233, 0.2); color: var(--accent-sky); }
	.message-type-badge.review_ready { background: rgba(245, 158, 11, 0.15); color: var(--accent-amber); }
	.message-type-badge.agree, .message-type-badge.contribution { background: rgba(16, 185, 129, 0.2); color: var(--accent-green); }
	.message-type-badge.concern { background: rgba(245, 158, 11, 0.2); color: var(--accent-amber); }
	.message-type-badge.block { background: rgba(239, 68, 68, 0.2); color: var(--accent-red); }
	.message-type-badge.stand_aside, .message-type-badge.decline { background: rgba(107, 114, 128, 0.15); color: var(--accent-gray); }
	.message-type-badge.proposal { background: rgba(59, 130, 246, 0.2); color: var(--accent-blue); }
	.message-type-badge.consent { background: rgba(16, 185, 129, 0.2); color: var(--accent-green); }
	.message-type-badge.synthesis { background: rgba(245, 158, 11, 0.2); color: var(--accent-amber); }
	.message-type-badge.follow_up { background: rgba(245, 158, 11, 0.15); color: var(--accent-amber); }
	.message-type-badge.reply { background: rgba(16, 185, 129, 0.2); color: var(--accent-green); }
	.message-type-badge.triage_result { background: rgba(99, 102, 241, 0.2); color: var(--accent-indigo); }
	.message-type-badge.gap_detected { background: rgba(239, 68, 68, 0.15); color: var(--accent-red); }
	.message-type-badge.thread_close { background: rgba(107, 114, 128, 0.2); color: var(--accent-gray); }

	.message-time {
		margin-left: auto;
		font-size: 0.7rem;
		color: var(--color-text-muted);
	}

	.message-card.follow_up {
		border-color: #f59e0b;
		background: rgba(245, 158, 11, 0.05);
	}

	.message-card.reply {
		border-color: #10b981;
		background: rgba(16, 185, 129, 0.05);
	}

	.message-card.triage_result {
		border-color: #6366f1;
		background: rgba(99, 102, 241, 0.05);
	}

	.triage-agents {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
		margin-top: 0.5rem;
	}

	.after-answer-note {
		font-size: 0.65rem;
		color: var(--color-text-muted);
		font-style: italic;
	}

	.message-card.after-answer {
		opacity: 0.7;
	}

	.message-card.subcommittee {
		padding-top: 0.375rem;
		padding-bottom: 0.375rem;
	}

	.triage-agent-badge {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		font-size: 0.7rem;
		font-weight: 500;
		padding: 0.125rem 0.5rem;
		border-radius: 1rem;
		background: rgba(99, 102, 241, 0.15);
		color: var(--accent-indigo);
		cursor: help;
	}

	.triage-confidence {
		font-size: 0.6rem;
		opacity: 0.8;
		font-weight: 600;
	}

	.triage-overall {
		font-size: 0.65rem;
		color: var(--color-text-muted);
		padding: 0.125rem 0.5rem;
	}

	.thread-started {
		font-size: 0.7rem;
		color: var(--color-text-muted);
		margin-top: -0.5rem;
	}

	.thread-status-label {
		font-size: 0.7rem;
		font-weight: 600;
		text-transform: uppercase;
	}

	.thread-status-label.open { color: var(--accent-green); }
	.thread-status-label.synthesized { color: var(--accent-amber); }
	.thread-status-label.closed { color: var(--color-text-muted); }

	.thread-actions {
		display: flex;
		align-items: center;
		gap: 0.25rem;
		margin-left: auto;
		flex-wrap: nowrap;
	}

	.status-icon {
		display: inline-flex; align-items: center; justify-content: center;
		width: 1.75rem; height: 1.75rem; color: var(--color-text-muted); cursor: default;
	}
	.status-icon.working { color: var(--accent-green); }
	.status-icon.answered { color: var(--accent-green); }
	.status-icon.failed { color: var(--accent-red); }

	.action-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 1.75rem;
		height: 1.75rem;
		background: none;
		border: none;
		padding: 0;
		cursor: pointer;
		font-size: 0.875rem;
		opacity: 0.7;
		transition: opacity 0.2s;
		border-radius: 0.25rem;
		color: var(--color-text-muted);
	}

	.action-btn:hover {
		opacity: 1;
		background-color: var(--color-bg-secondary);
	}

	.thread-query-full {
		display: flex;
		align-items: flex-start;
		gap: 0.5rem;
		font-size: 0.9rem;
		color: var(--color-text);
		padding: 0.75rem;
		background: var(--color-bg-secondary);
		border-radius: 0.5rem;
		border: 1px solid var(--color-border);
	}

	.thread-query-full span {
		flex: 1;
	}

	.thread-id-copy {
		flex-shrink: 0;
		background: none;
		border: 1px solid var(--color-border);
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
		font-size: 0.7rem;
		font-family: var(--font-mono);
		color: var(--color-text-muted);
		cursor: pointer;
		opacity: 0.5;
		transition: opacity 0.15s;
	}

	.thread-id-copy:hover {
		opacity: 1;
		border-color: var(--color-primary);
	}

	.message-content {
		font-size: 0.8rem;
		color: var(--color-text);
		line-height: 1.5;
	}

	/* Collapsed synthesis: cap height and fade the cut edge so the truncation reads
	   as deliberate. Expanding (openSynthesis) drops .collapsed and shows the full text.
	   Mirrors the fitness transcript view to keep the two synthesis renderers in lockstep. */
	.synth-body.collapsed {
		max-height: 12em;
		overflow: hidden;
		-webkit-mask-image: linear-gradient(to bottom, black 70%, transparent 100%);
		mask-image: linear-gradient(to bottom, black 70%, transparent 100%);
	}
	/* Inline expand/collapse toggle for a long synthesis. */
	.finding-toggle {
		background: none;
		border: none;
		cursor: pointer;
		padding: 0 0.3rem;
		color: var(--color-cyan);
		font-size: 0.78rem;
		font-family: inherit;
	}
	.finding-toggle:hover {
		text-decoration: underline;
	}
	/* Download action for a spilled artifact, sits next to the more/less toggle. */
	.artifact-dl {
		color: var(--color-cyan);
		font-size: 0.78rem;
		text-decoration: none;
		padding: 0 0.3rem;
		border-bottom: 1px dotted var(--color-cyan);
	}
	.artifact-dl:hover {
		border-bottom-style: solid;
	}

	.message-content.markdown :global(p) {
		margin: 0.25rem 0;
	}

	.message-content.markdown :global(pre) {
		background: rgba(0, 0, 0, 0.2);
		padding: 0.5rem;
		border-radius: 0.375rem;
		overflow-x: auto;
		font-size: 0.75rem;
	}

	.message-content.markdown :global(code) {
		font-size: 0.75rem;
		background: rgba(0, 0, 0, 0.15);
		padding: 0.1rem 0.3rem;
		border-radius: 0.2rem;
	}

	.message-content.markdown :global(pre code) {
		background: none;
		padding: 0;
	}

	.message-content.markdown :global(ul), .message-content.markdown :global(ol) {
		margin: 0.25rem 0;
		padding-left: 1.25rem;
	}

	.message-content.markdown :global(strong) {
		font-weight: 600;
	}

	.message-content.markdown :global(h1),
	.message-content.markdown :global(h2),
	.message-content.markdown :global(h3) {
		font-size: 0.85rem;
		font-weight: 600;
		margin: 0.5rem 0 0.25rem;
	}

	.message-content.markdown :global(hr) {
		border: none;
		border-top: 1px solid var(--color-border);
		margin: 0.5rem 0;
	}

	.tools-used {
		display: flex;
		gap: 0.375rem;
		flex-wrap: wrap;
		margin-top: 0.5rem;
	}

	.tool-badge {
		font-size: 0.6rem;
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
		background: rgba(139, 92, 246, 0.2);
		color: var(--accent-violet);
		font-family: ui-monospace, monospace;
	}

	.tech-badges {
		display: flex;
		gap: 0.375rem;
		flex-wrap: wrap;
		margin-bottom: 0.5rem;
	}

	.tech-badge {
		font-size: 0.6rem;
		padding: 0.125rem 0.5rem;
		border-radius: 1rem;
		background: rgba(168, 85, 247, 0.15);
		color: var(--accent-purple);
		font-weight: 500;
	}

	/* Empty slot at the bottom of an open discussion, kept for the follow-up input. */
	.reply-bar {
		display: flex;
		gap: 0.5rem;
		flex-shrink: 0;
	}

	.no-selection {
		display: flex;
		align-items: center;
		justify-content: center;
		height: 100%;
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.span-graph-panel {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		max-height: 50vh;
		overflow-x: hidden;
		overflow-y: auto;
		flex-shrink: 0;
	}

	.action-btn.active-toggle {
		color: var(--accent-blue);
		opacity: 1;
		background-color: rgba(59, 130, 246, 0.1);
	}
</style>
