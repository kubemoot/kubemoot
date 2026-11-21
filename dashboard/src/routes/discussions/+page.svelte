<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { marked } from 'marked';
	import { crewDirectory, threads, sortedThreads, discussionsConnected, historyLoaded, initDiscussions, ensureThreadLoaded, loadOlder, loadingOlder, hasOlder, showStandAsides, showDebugDetail, namespace } from '#lib/stores/index.js';
	import type { Thread } from '#lib/stores/index.js';
	import { tryCrewScope, type CrewScope } from '#lib/crewScope.js';
	import { crewEntry, crewLabel, crewTooltip } from '#lib/crew-display-name.js';
	import { DiscussionSpanGraph, MessageStatsLine, PlacementPanel, ThreadSummaryPanel } from '#lib/components/discussions/index.js';
	import { debugMessageCount, visibleMessages, afterAnswerMessages } from '#lib/conversation-view.js';
	import { placementCallsOf } from '#lib/placement-panel.js';
	import { formatTimestamp, messageStats, summarizeThread } from '#lib/discussion-timing.js';
	import { formatElapsed } from '#lib/duration-format.js';
	import { SYNTHESIS_COLLAPSE_CHARS, artifactKey, artifactHref } from '#lib/discussion-artifacts.js';
	import { aggregateAgents, renderAgentSummary } from '#lib/discussion-agent-summary.js';
	import { threadStartMs as messagesStartMs } from '#lib/discussion-spans.js';
	import { buildGpuDisplayMap } from '#lib/gpu-display.js';
	import * as tips from '#lib/discussion-actions.js';
	import { CommandHint, ConversationDownload } from '#lib/components/common/index.js';
	import { askCommand, listConversationsCommand, replayConversationCommand } from '#lib/cli-commands.js';
	import { threadConversationId } from '#lib/conversation-export.js';
	import { fetchDashboardVersion, versionLabel, VERSION_UNKNOWN } from '#lib/dashboard-version.js';

	// Configure marked for safe inline rendering
	marked.setOptions({ breaks: true, gfm: true });

	interface ChannelInfo {
		name: string;
		color: string;
		agentCount: number;
	}

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
	const visibleThreads = $derived(
		$namespace === '' ? $sortedThreads : $sortedThreads.filter((t) => t.namespace === $namespace)
	);

	// Per-synthesis expand toggle (keyed by thread:messageId): a long synthesis
	// collapses to a faded preview by default and expands to the full text on demand.
	// Same pattern + helpers as the fitness transcript view (#lib/discussion-artifacts).
	let openSynthesis = $state<Record<string, boolean>>({});

	// Thread actions state
	let copiedThread = $state(false);
	let showSpanGraph = $state(false);
	let dashboardVersion = $state(VERSION_UNKNOWN);
	// Server-id -> discovered GPU model (e.g. "ollama-rig1" -> "RTX 4090"), so the
	// span graph shows the human GPU name instead of the raw server id. Sourced
	// from the operator's DCGM discovery (ModelServer.status.capacity.gpuModel);
	// no GPU model is hardcoded.
	let gpuDisplayMap = $state<Record<string, string>>({});

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
	const selectedConversationId = $derived(selectedThread ? threadConversationId(selectedThread) : undefined);
	const selectedThreadScope = $derived(selectedThread ? tryCrewScope(selectedThread.namespace, selectedThread.crew) : null);
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
	const debugCount = $derived(debugMessageCount(selectedThread?.messages ?? []));
	const afterAnswer = $derived(afterAnswerMessages(selectedThread?.messages ?? []));

	function debugToggleTitle(on: boolean, hidden: number): string {
		if (on) return 'Hide debug detail';
		const more = hidden > 0 ? `${hidden} more messages, ` : '';
		return `Show debug detail (${more}timings, tokens, placement)`;
	}

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
		copiedThread = true;
		setTimeout(() => { copiedThread = false; }, 2000);
	}

	// Build server-id -> GPU model map from the operator-discovered ModelServer
	// status. Keys on BOTH the CR name and the endpoint namespace, because span
	// metadata may carry either form (GpuLabels.fromServer vs fromEndpoint).
	async function loadGpuLabels() {
		try {
			const res = await fetch(`${resolve('/api/kubemoot/modelservers')}?namespace=`);
			const data = await res.json();
			const items = Array.isArray(data?.items) ? data.items : [];
			gpuDisplayMap = buildGpuDisplayMap(items);
		} catch (e) {
			console.error('Failed to load GPU labels:', e);
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

	let timelineEl = $state<HTMLElement | null>(null);
	let copiedThreadId = $state(false);

	async function copyThreadId() {
		if (!selectedThread) return;
		const text = `Thread id: ${selectedThread.threadId} | ${selectedThread.userQuery}`;
		await navigator.clipboard.writeText(text);
		copiedThreadId = true;
		setTimeout(() => { copiedThreadId = false; }, 2000);
	}

	// Auto-scroll to bottom when the selected thread gains messages.
	$effect(() => {
		// Reading the message count registers the reactive dependency, so this
		// effect re-runs as new messages arrive.
		const count = selectedThread?.messages.length ?? 0;
		if (timelineEl && count > 0) {
			// Defer so we scroll after the DOM updates.
			requestAnimationFrame(() => {
				if (timelineEl) {
					timelineEl.scrollTop = timelineEl.scrollHeight;
				}
			});
		}
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
		loadGpuLabels();
		fetchDashboardVersion(fetch, resolve('/api/version')).then((v) => { dashboardVersion = v; });
		// A link from Messages opens its thread: /discussions?thread=<id>
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

<div class="discussions-page">
	<div class="page-header">
		<h1>Agent Discussions</h1>
		<div class="header-actions">
			<div class="connection-status" class:connected={$discussionsConnected}>
				<span class="status-dot"></span>
				{$discussionsConnected ? 'Connected' : 'Disconnected'}
			</div>
		</div>
	</div>

	<div class="discussions-layout">
		<div class="thread-list" onscroll={onListScroll}>
			{#if !$historyLoaded && $sortedThreads.length === 0}
				<div class="empty-state">Loading discussion history...</div>
			{:else if visibleThreads.length === 0}
				<div class="empty-state">
					{#if selectedCrew}
						No discussions for crew "{crewLabel($crewDirectory, $namespace, selectedCrew)}". Pick "All crews" to see all.
					{:else}
						No discussions yet. Start one with <code>{askCommand(selectedScope?.crew ?? 'CREW', selectedScope?.namespace ?? 'NAMESPACE')}</code>.
					{/if}
				</div>
			{:else}
				{#each visibleThreads as thread}
					<div class="thread-row">
					<button
						class="thread-card"
						class:selected={selectedThreadId === thread.threadId}
						onclick={() => selectThread(thread.threadId)}
					>
						<div class="thread-header">
							{#if thread.crew}
								<span class="crew-badge" title={crewTooltip($crewDirectory, thread.namespace, thread.crew)}>{$namespace === '' && thread.namespace ? `${thread.namespace}/` : ''}{crewLabel($crewDirectory, thread.namespace, thread.crew)}</span>
							{/if}
							<span
								class="channel-badge"
								style="background-color: {channelColors[thread.channel] || '#6b7280'}"
							>
								{thread.channel}
							</span>
							<span class="thread-status" class:open={thread.status === 'open'}
								class:synthesized={thread.status === 'synthesized'}
								class:closed={thread.status === 'closed'}>
								{statusLabel(thread.status)}
							</span>
						</div>
						<div class="thread-query">{thread.userQuery}</div>
						<div class="thread-meta">
							<span>
								{thread.agreeCount} agree{#if thread.concernCount > 0}, {thread.concernCount} concern{thread.concernCount !== 1 ? 's' : ''}{/if}{#if thread.blockCount > 0}, {thread.blockCount} block{thread.blockCount !== 1 ? 's' : ''}{/if}{#if thread.standAsideCount > 0}, {thread.standAsideCount} aside{/if}
							</span>
							<span title={thread.startedAt}>{formatTimestamp(thread.startedAt)}</span>
						</div>
					</button>
					</div>
				{/each}
				{#if $hasOlder}
					<button class="load-older" disabled={$loadingOlder} onclick={() => loadOlder()}>
						{$loadingOlder ? 'Loading older threads...' : 'Load older threads'}
					</button>
				{/if}
			{/if}
		</div>

		<div class="thread-detail">
			{#if selectedThread}
				<div class="thread-detail-header">
					{#if selectedThread.crew}
						<span class="crew-badge" title={crewTooltip($crewDirectory, selectedThread.namespace, selectedThread.crew)}>{$namespace === '' && selectedThread.namespace ? `${selectedThread.namespace}/` : ''}{crewLabel($crewDirectory, selectedThread.namespace, selectedThread.crew)}</span>
					{/if}
					{#if selectedThread.crewVersion}
						<span class="crew-version-badge" title="Crew chart version">v{selectedThread.crewVersion}</span>
					{/if}
					<span
						class="channel-badge large"
						style="background-color: {channelColors[selectedThread.channel] || '#6b7280'}"
					>
						{selectedThread.channel}
					</span>
					<span class="thread-status-label" class:open={selectedThread.status === 'open'}
						class:synthesized={selectedThread.status === 'synthesized'}
						class:closed={selectedThread.status === 'closed'}>
						{statusLabel(selectedThread.status)}
					</span>
					{#if selectedThread.startedBy === 'human'}
						<span class="human-badge">Human-initiated</span>
					{/if}
					<div class="thread-actions">
						<button class="action-btn" class:active-toggle={showSpanGraph} onclick={() => { showSpanGraph = !showSpanGraph; }} title={tips.SPAN_GRAPH_TOOLTIP} aria-label="Toggle span graph">
							<svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
								<rect x="1" y="2" width="10" height="3" rx="1" fill="currentColor" opacity="0.8" />
								<rect x="1" y="7" width="14" height="3" rx="1" fill="currentColor" opacity="0.6" />
								<rect x="1" y="12" width="7" height="3" rx="1" fill="currentColor" opacity="0.4" />
							</svg>
						</button>
						<button class="action-btn" class:active-toggle={$showDebugDetail} onclick={() => showDebugDetail.toggle()} title={debugToggleTitle($showDebugDetail, debugCount)} aria-label="Show debug detail" aria-pressed={$showDebugDetail}>
							<svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
								<ellipse cx="8" cy="9.5" rx="3" ry="4" fill="currentColor" opacity="0.8" />
								<path d="M5 6.5L3 4.5M11 6.5L13 4.5M5 9.5H2M11 9.5H14M5.5 12.5L3.5 14.5M10.5 12.5L12.5 14.5M6.5 5.5C6.5 4 7 3 8 3S9.5 4 9.5 5.5" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" />
							</svg>
						</button>
						{#if selectedConversationId}
							<ConversationDownload scope={selectedThreadScope} ids={[selectedConversationId]} endpoint={resolve('/api/kubemoot/conversation-export')} label="Download conversation" />
						{/if}
						<button class="action-btn" onclick={copyThread} title={tips.COPY_THREAD_TOOLTIP} aria-label="Copy thread">
							{copiedThread ? '✓' : '📋'}
						</button>
					</div>
				</div>
				<div class="thread-query-full" title={selectedThread.threadId}>
					<span>{selectedThread.userQuery}</span>
					<button class="thread-id-copy" onclick={copyThreadId} title={tips.COPY_ID_TOOLTIP}>
						{copiedThreadId ? '✓' : '#'}
					</button>
				</div>
				{#if selectedSummary?.startedAt && formatTimestamp(selectedSummary.startedAt)}
					<div class="thread-started" title={selectedSummary.startedAt}>
						Started {formatTimestamp(selectedSummary.startedAt)}; times below are since then
					</div>
				{/if}

				{#if showSpanGraph && selectedThread}
					<div class="span-graph-panel">
						<DiscussionSpanGraph
							messages={selectedThread.messages}
							threadId={selectedThread.threadId}
							userQuery={selectedThread.userQuery}
							version={dashboardVersion}
							crew={selectedThread.crew}
							{gpuDisplayMap}
						/>
					</div>
				{/if}

				<div class="message-timeline" bind:this={timelineEl}>
					{#each timelineMessages as msg, msgIdx}
						{@const stats = selectedStats.get(msg) ?? {}}
						<div
							class="message-card {msg.messageType}"
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
								{#if aKey}<a class="artifact-dl" href={artifactHref(aKey)} download title="Download the full spilled artifact ({aKey})">download</a>{/if}
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
								<MessageStatsLine {stats} acceleratorNames={gpuDisplayMap} />
							{/if}
						</div>
					{/each}
					{#if $showDebugDetail && selectedSummary && (selectedSummary.endedAt || selectedSummary.participants.length > 0)}
						<ThreadSummaryPanel summary={selectedSummary} {agentLabel} />
					{/if}
					{#if $showDebugDetail && selectedPlacementCalls.length > 0}
						<PlacementPanel calls={selectedPlacementCalls} {agentLabel} />
					{/if}
				</div>

				<div class="reply-bar">
					<CommandHint
						label="Ask a follow-up from a terminal (find the conversation id with {listConversationsCommand(selectedThread.crew ?? 'CREW', selectedThread.namespace)}):"
						command={askCommand(selectedThread.crew ?? 'CREW', selectedThread.namespace, 'CONVERSATION_ID')}
					/>
					{#if selectedConversationId}
						<CommandHint label="Ask this conversation's first question again, in a new conversation:" command={replayConversationCommand(selectedThread.crew ?? 'CREW', selectedConversationId, selectedThread.namespace)} />
					{/if}
				</div>
			{:else}
				<div class="no-selection">
					Select a discussion thread to view its timeline
				</div>
			{/if}
		</div>
	</div>
</div>

<style>
	.discussions-page {
		display: flex;
		flex-direction: column;
		height: 100%;
		padding: 1.5rem;
		gap: 1rem;
	}

	.page-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-shrink: 0;
	}

	h1 {
		font-size: 1.25rem;
		font-weight: 600;
		color: var(--color-text);
		margin: 0;
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: 1rem;
	}

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

	.discussions-layout {
		display: flex;
		gap: 1rem;
		flex: 1;
		min-height: 0;
	}

	.thread-list {
		width: 360px;
		flex-shrink: 0;
		overflow-y: auto;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
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

	.thread-card {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 0.75rem;
		cursor: pointer;
		text-align: left;
		width: 100%;
		transition: all 0.15s;
		color: var(--color-text);
		font-family: inherit;
		font-size: inherit;
	}

	.thread-card:hover {
		border-color: var(--color-primary);
	}

	.thread-card.selected {
		border-color: var(--color-primary);
		background: rgba(59, 130, 246, 0.1);
	}

	.thread-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 0.5rem;
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

	.crew-version-badge {
		display: inline-block;
		padding: 0.125rem 0.4rem;
		border-radius: 1rem;
		font-size: 0.6rem;
		font-weight: 600;
		background: rgba(148, 163, 184, 0.15);
		color: var(--color-text-muted, #94a3b8);
		font-family: var(--font-mono);
	}

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
		font-size: 0.65rem;
		font-weight: 500;
		text-transform: uppercase;
	}

	.thread-status.open { color: #10b981; }
	.thread-status.synthesized { color: #f59e0b; }
	.thread-status.closed { color: var(--color-text-muted); }

	.thread-query {
		font-size: 0.8rem;
		color: var(--color-text);
		margin-bottom: 0.5rem;
		display: -webkit-box;
		-webkit-line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}

	.thread-meta {
		display: flex;
		justify-content: space-between;
		font-size: 0.7rem;
		color: var(--color-text-muted);
	}

	.thread-detail {
		flex: 1;
		overflow-y: auto;
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.thread-detail-header {
		display: flex;
		align-items: center;
		gap: 1rem;
	}

	.human-badge {
		font-size: 0.65rem;
		padding: 0.125rem 0.5rem;
		border-radius: 0.25rem;
		background: rgba(16, 185, 129, 0.2);
		color: #34d399;
		font-weight: 500;
	}

	.message-timeline {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		flex: 1;
		overflow-y: auto;
	}

	.message-card {
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
		color: #34d399;
	}

	.message-type-badge {
		font-size: 0.6rem;
		font-weight: 600;
		text-transform: uppercase;
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
	}

	.message-type-badge.thread_start { background: rgba(59, 130, 246, 0.2); color: #60a5fa; }
	.message-type-badge.advisory { background: rgba(168, 85, 247, 0.2); color: #c084fc; }
	.message-type-badge.advisory_ready { background: rgba(14, 165, 233, 0.2); color: #38bdf8; }
	.message-type-badge.review_ready { background: rgba(245, 158, 11, 0.15); color: #fbbf24; }
	.message-type-badge.agree, .message-type-badge.contribution { background: rgba(16, 185, 129, 0.2); color: #34d399; }
	.message-type-badge.concern { background: rgba(245, 158, 11, 0.2); color: #fbbf24; }
	.message-type-badge.block { background: rgba(239, 68, 68, 0.2); color: #f87171; }
	.message-type-badge.stand_aside, .message-type-badge.decline { background: rgba(107, 114, 128, 0.15); color: #6b7280; }
	.message-type-badge.proposal { background: rgba(59, 130, 246, 0.2); color: #60a5fa; }
	.message-type-badge.consent { background: rgba(16, 185, 129, 0.2); color: #34d399; }
	.message-type-badge.synthesis { background: rgba(245, 158, 11, 0.2); color: #fbbf24; }
	.message-type-badge.follow_up { background: rgba(245, 158, 11, 0.15); color: #fbbf24; }
	.message-type-badge.reply { background: rgba(16, 185, 129, 0.2); color: #34d399; }
	.message-type-badge.triage_result { background: rgba(99, 102, 241, 0.2); color: #818cf8; }
	.message-type-badge.gap_detected { background: rgba(239, 68, 68, 0.15); color: #f87171; }
	.message-type-badge.thread_close { background: rgba(107, 114, 128, 0.2); color: #9ca3af; }

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
		color: #a5b4fc;
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

	.thread-status-label.open { color: #10b981; }
	.thread-status-label.synthesized { color: #f59e0b; }
	.thread-status-label.closed { color: var(--color-text-muted); }

	.thread-actions {
		display: flex;
		align-items: center;
		gap: 0.25rem;
		margin-left: auto;
	}

	.action-btn {
		background: none;
		border: none;
		padding: 0.25rem 0.375rem;
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
		color: #a78bfa;
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
		color: #c084fc;
		font-weight: 500;
	}

	.reply-bar {
		display: flex;
		gap: 0.5rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--color-border);
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
		color: #3b82f6;
		opacity: 1;
		background-color: rgba(59, 130, 246, 0.1);
	}
</style>
