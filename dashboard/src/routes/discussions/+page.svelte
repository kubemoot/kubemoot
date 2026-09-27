<script lang="ts">
	import { onMount } from 'svelte';
	import { base } from '$app/paths';
	import { marked } from 'marked';
	import type { DiscussionMessage } from '$types/kubemoot.js';
	import { threads, sortedThreads, discussionsConnected, historyLoaded, initDiscussions, addDiscussionMessage, removeThread, pinnedThreadIds, loadPinnedThreads, pinThread, unpinThread, showStandAsides, namespace, threadScope } from '$lib/stores';
	import type { Thread } from '$lib/stores';
	import { discussSubject, tryCrewScope, type CrewScope } from '$lib/crewScope';
	import { DiscussionSpanGraph } from '$lib/components/discussions';
	import { SYNTHESIS_COLLAPSE_CHARS, artifactKey, artifactHref } from '$lib/discussion-artifacts';

	// Configure marked for safe inline rendering
	marked.setOptions({ breaks: true, gfm: true });

	interface ChannelInfo {
		name: string;
		color: string;
		agentCount: number;
	}

	let selectedThreadId = $state<string | null>(null);

	// Crews (namespace+crew pairs) for the global top-bar "Crew:" selector. The
	// `namespace` store holds the selected crew NAMESPACE ('' = All crews). Threads
	// carry the namespace from their subject, so the list filters by namespace and
	// same-named crews in other namespaces stay out of view.
	interface CrewNamespace { namespace: string; crew: string; }
	let crews = $state<CrewNamespace[]>([]);
	const selectedScope = $derived<CrewScope | null>(
		$namespace === ''
			? null
			: tryCrewScope($namespace, crews.find((c) => c.namespace === $namespace)?.crew)
	);
	const selectedCrew = $derived(selectedScope?.crew ?? null);
	const visibleThreads = $derived(
		$namespace === '' ? $sortedThreads : $sortedThreads.filter((t) => t.namespace === $namespace)
	);

	// New discussion form state
	let showNewForm = $state(false);
	let newChannel = $state('general');
	let newQuery = $state('');
	let sending = $state(false);

	// Reply state
	let replyText = $state('');

	// Per-synthesis expand toggle (keyed by thread:messageId): a long synthesis
	// collapses to a faded preview by default and expands to the full text on demand.
	// Same pattern + helpers as the fitness transcript view ($lib/discussion-artifacts).
	let openSynthesis = $state<Record<string, boolean>>({});

	// Thread actions state
	let copiedThread = $state(false);
	let deleteConfirm = $state(false);
	let showSpanGraph = $state(false);
	let dashboardVersion = $state('dev');
	// Provider-id -> discovered GPU model (e.g. "ollama-rig1" -> "RTX 4090"), so the
	// span graph shows the human GPU name instead of the raw provider id. Sourced
	// from the operator's DCGM discovery (ModelProvider.status.capacity.gpuModel);
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

	// Maps a raw thread status to its human label. Shared by the thread-list row
	// and the detail pane so both stay in lockstep (open->Active, synthesized->Answered).
	function statusLabel(status: string): string {
		if (status === 'open') return 'Active';
		if (status === 'synthesized') return 'Answered';
		return 'Closed';
	}

	function formatTime(ts: string): string {
		try {
			return new Date(ts).toLocaleString([], {
				month: 'short', day: 'numeric',
				hour: '2-digit', minute: '2-digit', second: '2-digit'
			});
		} catch {
			return ts;
		}
	}

	function selectThread(threadId: string) {
		selectedThreadId = threadId;
		deleteConfirm = false;
	}

	function generateId(): string {
		return crypto.randomUUID();
	}

	// The subject for a message on an existing thread: the thread's own namespace
	// and crew, falling back to the selected crew for a thread whose scope is not
	// yet known. Throws when neither is available rather than publishing unscoped.
	function threadSubject(thread: Thread, threadId: string = thread.threadId): string {
		const scope = threadScope(thread) ?? selectedScope;
		if (!scope) throw new Error(`discussion ${thread.threadId} has no namespace and crew; select a crew first`);
		return discussSubject(scope, thread.channel, threadId);
	}

	async function publishMessage(subject: string, data: object) {
		const res = await fetch(`${base}/api/nats/publish`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ subject, data: JSON.stringify(data) })
		});
		if (!res.ok) throw new Error('Failed to publish');
	}

	async function startNewDiscussion() {
		if (!newQuery.trim() || sending || !selectedScope) return;
		sending = true;
		try {
			const threadId = generateId();
			const subject = discussSubject(selectedScope, newChannel, threadId);
			const msg: DiscussionMessage = {
				messageId: generateId(),
				threadId,
				agentName: 'human',
				messageType: 'thread_start',
				content: newQuery.trim(),
				channel: newChannel,
				timestamp: new Date().toISOString(),
				metadata: { userQuery: newQuery.trim() }
			};
			// Optimistic: show message immediately
			addDiscussionMessage(msg, subject);
			selectedThreadId = threadId;
			await publishMessage(subject, msg);
			newQuery = '';
			showNewForm = false;
		} catch (e) {
			console.error('Failed to start discussion:', e);
		} finally {
			sending = false;
		}
	}

	async function sendReply() {
		if (!replyText.trim() || !selectedThread || sending) return;
		sending = true;
		try {
			const subject = threadSubject(selectedThread);
			const msg: DiscussionMessage = {
				messageId: generateId(),
				threadId: selectedThread.threadId,
				agentName: 'human',
				messageType: 'reply',
				content: replyText.trim(),
				channel: selectedThread.channel,
				timestamp: new Date().toISOString()
			};
			// Optimistic: show message immediately
			addDiscussionMessage(msg, subject);
			await publishMessage(subject, msg);
			replyText = '';
		} catch (e) {
			console.error('Failed to send reply:', e);
		} finally {
			sending = false;
		}
	}

	function handleReplyKeydown(event: KeyboardEvent) {
		if (event.key === 'Enter' && !event.shiftKey) {
			event.preventDefault();
			sendReply();
		}
	}

	function handleNewKeydown(event: KeyboardEvent) {
		if (event.key === 'Enter' && !event.shiftKey) {
			event.preventDefault();
			startNewDiscussion();
		}
	}

	// Per-agent aggregation accumulated across a thread's messages for the
	// clipboard "Agent Summary" table.
	interface AgentAgg {
		signal: string;
		gpu: string;
		provider: string;
		pickReason: string;
		startMs: number;
		endMs: number;
		inferenceMs: number;
		tools: Set<string>;
	}

	// Signal priority for the per-agent summary: a later, higher-priority signal
	// (synthesis > block > concern > agree > advisory > stand_aside) wins as the
	// agent's representative signal. Module scope keeps the aggregation loop simple.
	const SIGNAL_PRIORITY: Record<string, number> = {
		synthesis: 10, block: 9, concern: 8, agree: 7, advisory: 6, stand_aside: 2
	};

	async function fetchDashboardVersion(): Promise<string> {
		try {
			const res = await fetch(`${base}/api/version`);
			const data = await res.json();
			return data.version || 'dev';
		} catch {
			return 'dev';
		}
	}

	function buildThreadHeader(thread: Thread, version: string): string {
		let text = `# ${thread.userQuery}\n`;
		text += `**Discussion**: ${thread.threadId}\n`;
		text += `**Kubemoot Dashboard**: v${version}\n`;
		if (thread.namespace) text += `**Namespace**: ${thread.namespace}\n`;
		if (thread.crew) text += `**Crew**: ${thread.crew}\n`;
		if (thread.crewVersion) text += `**Crew version**: ${thread.crewVersion}\n`;
		text += `Channel: ${thread.channel} | Status: ${thread.status} | Started: ${thread.startedAt}\n\n`;
		return text;
	}

	function newAgentAgg(msg: DiscussionMessage, ts: number): AgentAgg {
		return {
			signal: msg.messageType,
			gpu: msg.metadata?.gpuLabel || '',
			provider: msg.metadata?.provider || '',
			// FitPredictor v2 reasoning ("warm, slot 1/2 | SR=0.92 …"). Surfaced as a
			// title attribute on the GPU/provider column so it's a hover tooltip,
			// not a column expansion that would push the table wider.
			pickReason: msg.metadata?.pickReason || '',
			startMs: ts, endMs: ts,
			inferenceMs: msg.metadata?.inferenceMs || 0,
			tools: new Set(msg.metadata?.toolsUsed || [])
		};
	}

	function mergeAgentMessage(existing: AgentAgg, msg: DiscussionMessage, ts: number) {
		existing.startMs = Math.min(existing.startMs, ts);
		existing.endMs = Math.max(existing.endMs, ts);
		const p = SIGNAL_PRIORITY[msg.messageType] ?? 0;
		if (p > (SIGNAL_PRIORITY[existing.signal] ?? 0)) existing.signal = msg.messageType;
		if (msg.metadata?.gpuLabel) existing.gpu = msg.metadata.gpuLabel;
		if (msg.metadata?.provider) existing.provider = msg.metadata.provider;
		if (msg.metadata?.pickReason) existing.pickReason = msg.metadata.pickReason;
		if (msg.metadata?.inferenceMs && msg.metadata.inferenceMs > existing.inferenceMs) existing.inferenceMs = msg.metadata.inferenceMs;
		if (msg.metadata?.toolsUsed) msg.metadata.toolsUsed.forEach(t => existing.tools.add(t));
	}

	// `provider` (e.g. "ollama-gpu", "ollama-rig1") is the JIT-selected
	// ModelProvider name from the agent runtime's ProviderSelector. Carries
	// the per-call truth: WHICH provider this agent actually used for THIS
	// inference. Falls back to the legacy `gpuLabel` (which is the
	// reconcile-time static label) when a message lacks the provider field
	// — pre-Card-#2 messages and stand-aside paths without inference.
	// See Card #4 of [[Epic - JIT GPU Scheduling]].
	function aggregateAgents(thread: Thread, t0: number): Map<string, AgentAgg> {
		const agentMap = new Map<string, AgentAgg>();
		for (const msg of thread.messages) {
			const ts = new Date(msg.timestamp).getTime() - t0;
			const existing = agentMap.get(msg.agentName);
			if (existing) {
				mergeAgentMessage(existing, msg, ts);
			} else {
				agentMap.set(msg.agentName, newAgentAgg(msg, ts));
			}
		}
		return agentMap;
	}

	function renderAgentSummary(agentMap: Map<string, AgentAgg>): string {
		let text = `## Agent Summary\n`;
		text += `| Agent | Signal | GPU | Duration | Inference | Tools |\n`;
		text += `|-------|--------|-----|----------|-----------|-------|\n`;
		const fmtMs = (ms: number) => ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`;
		for (const [agent, data] of agentMap) {
			const name = agent === 'human' ? 'You' : agent.replace('homelab-', '');
			const dur = fmtMs(data.endMs - data.startMs);
			const inf = data.inferenceMs > 0 ? fmtMs(data.inferenceMs) : '-';
			const tools = data.tools.size > 0 ? Array.from(data.tools).join(', ') : '-';
			// Per-call provider attribution wins over reconcile-time gpuLabel
			// when present — shows the JIT-selected ModelProvider name, the
			// truth of where this inference actually ran. FitPredictor v2's
			// pickReason (if present) carries the per-call decision narrative
			// ("warm, slot 1/2 | SR=0.92 over 23 samples, EMA latency 4200ms").
			const where = data.provider || data.gpu || '-';
			const why = data.pickReason ? `<br/>_${data.pickReason}_` : '';
			text += `| ${name} | ${data.signal} | ${where}${why} | ${dur} | ${inf} | ${tools} |\n`;
		}
		text += `\n`;
		return text;
	}

	function renderMessageTimeline(thread: Thread): string {
		let text = `## Messages\n`;
		for (const msg of thread.messages) {
			const agent = msg.agentName === 'human' ? 'You' : msg.agentName.replace('homelab-', '');
			text += `**${agent}** [${messageTypeLabels[msg.messageType] || msg.messageType}] (${formatTime(msg.timestamp)})\n`;
			if (msg.content) text += `${msg.content}\n`;
			text += '\n';
		}
		return text;
	}

	async function formatThreadForClipboard(thread: Thread): Promise<string> {
		const version = await fetchDashboardVersion();
		const threadStart = thread.messages.find(m => m.messageType === 'thread_start');
		const t0 = threadStart ? new Date(threadStart.timestamp).getTime() : new Date(thread.messages[0].timestamp).getTime();
		const agentMap = aggregateAgents(thread, t0);
		return buildThreadHeader(thread, version) + renderAgentSummary(agentMap) + renderMessageTimeline(thread);
	}

	async function copyThread() {
		if (!selectedThread) return;
		const text = await formatThreadForClipboard(selectedThread);
		await navigator.clipboard.writeText(text);
		copiedThread = true;
		setTimeout(() => { copiedThread = false; }, 2000);
	}

	function isThreadPaused(thread: Thread): boolean {
		// Walk messages from latest to oldest; the most recent pause/resume wins.
		for (let i = thread.messages.length - 1; i >= 0; i--) {
			const m = thread.messages[i];
			if (m.messageType === 'thread_pause') return true;
			if (m.messageType === 'thread_resume') return false;
		}
		return false;
	}

	async function publishPauseSignal(thread: Thread, paused: boolean) {
		sending = true;
		try {
			const subject = threadSubject(thread);
			const msg: DiscussionMessage = {
				messageId: generateId(),
				threadId: thread.threadId,
				agentName: 'human',
				messageType: paused ? 'thread_pause' : 'thread_resume',
				content: paused ? 'Discussion paused from dashboard' : 'Discussion resumed from dashboard',
				channel: thread.channel,
				timestamp: new Date().toISOString(),
				metadata: {}
			};
			addDiscussionMessage(msg, subject);
			await publishMessage(subject, msg);
		} catch (e) {
			console.error('Failed to publish pause signal:', e);
		} finally {
			sending = false;
		}
	}

	async function pauseThread() {
		if (!selectedThread || sending) return;
		await publishPauseSignal(selectedThread, true);
	}

	async function resumeThread() {
		if (!selectedThread || sending) return;
		await publishPauseSignal(selectedThread, false);
	}

	// Thread start time (ms) from the thread_start message, falling back to the
	// first message / startedAt. 0 when unknown.
	function threadStartMs(t: Thread): number {
		const start = t.messages.find(m => m.messageType === 'thread_start');
		const src = start?.timestamp ?? t.messages[0]?.timestamp ?? t.startedAt;
		const ms = src ? new Date(src).getTime() : NaN;
		return Number.isFinite(ms) ? ms : 0;
	}

	async function stopThread() {
		if (!selectedThread || sending) return;
		// Warn on an early stop: stopping in the first ~10s makes the coordinator
		// synthesize on the current (likely empty) signals, which reads as a
		// non-answer. Confirm before doing it.
		const startMs = threadStartMs(selectedThread);
		if (startMs && Date.now() - startMs < 10_000) {
			const secs = Math.max(1, Math.round((Date.now() - startMs) / 1000));
			if (!confirm(`This discussion started ${secs}s ago. Stopping now makes the coordinator synthesize on the current (possibly empty) signals, which usually reads as a non-answer. Stop anyway?`)) return;
		}
		sending = true;
		try {
			const subject = threadSubject(selectedThread);
			const msg: DiscussionMessage = {
				messageId: generateId(),
				threadId: selectedThread.threadId,
				agentName: 'human',
				messageType: 'stop_requested',
				content: 'Manual stop requested from dashboard',
				channel: selectedThread.channel,
				timestamp: new Date().toISOString(),
				metadata: { reason: 'manual_stop' }
			};
			addDiscussionMessage(msg, subject);
			await publishMessage(subject, msg);
		} catch (e) {
			console.error('Failed to stop discussion:', e);
		} finally {
			sending = false;
		}
	}

	async function replayThread() {
		if (!selectedThread || sending) return;
		sending = true;
		try {
			const original = selectedThread;
			const threadId = generateId();
			const subject = threadSubject(original, threadId);
			const msg: DiscussionMessage = {
				messageId: generateId(),
				threadId,
				agentName: 'human',
				messageType: 'thread_start',
				content: original.userQuery,
				channel: original.channel,
				timestamp: new Date().toISOString(),
				metadata: {
					userQuery: original.userQuery,
					replayOf: original.threadId
				}
			};
			addDiscussionMessage(msg, subject);
			selectedThreadId = threadId;
			await publishMessage(subject, msg);
		} catch (e) {
			console.error('Failed to replay discussion:', e);
		} finally {
			sending = false;
		}
	}

	async function togglePin() {
		if (!selectedThread) return;
		const threadId = selectedThread.threadId;
		if ($pinnedThreadIds.has(threadId)) {
			await unpinThread(threadId);
		} else {
			await pinThread(threadId);
		}
	}

	// Per-row remove from the list (mirrors the Fitness page ✕). Confirms, then
	// removeThread drops it from the timeline and purges its messages from NATS.
	async function removeDiscussion(threadId: string, e: Event) {
		e.stopPropagation();
		if (!confirm('Remove this discussion?\n\nIt is deleted from the timeline and its messages are purged. This cannot be undone.')) {
			return;
		}
		if (selectedThreadId === threadId) selectedThreadId = null;
		if ($pinnedThreadIds.has(threadId)) await unpinThread(threadId);
		await removeThread(threadId);
	}

	async function deleteThread() {
		if (!selectedThread) return;
		const threadId = selectedThread.threadId;
		if ($pinnedThreadIds.has(threadId)) {
			// Unpinning first lets us reuse the same purge path without a separate confirm.
			await unpinThread(threadId);
		}
		// Find current index within the visible (crew-filtered) list so we select
		// the natural next VISIBLE thread after deletion, not one hidden by the filter.
		const vis = visibleThreads;
		const idx = vis.findIndex(t => t.threadId === threadId);
		// Remove from local store and purge from NATS JetStream
		await removeThread(threadId);
		deleteConfirm = false;
		// Select next visible thread at the same position (or last if we deleted the last).
		const remaining = vis.filter(t => t.threadId !== threadId);
		if (remaining.length > 0) {
			const nextIdx = Math.min(idx, remaining.length - 1);
			selectedThreadId = remaining[nextIdx].threadId;
		} else {
			selectedThreadId = null;
		}
	}

	// Build provider-id -> GPU model map from the operator-discovered ModelProvider
	// status. Keys on BOTH the CR name and the endpoint namespace, because span
	// metadata may carry either form (GpuLabels.fromProvider vs fromEndpoint).
	async function loadGpuLabels() {
		try {
			const res = await fetch(`${base}/api/kubemoot/modelproviders?namespace=`);
			const data = await res.json();
			const items = Array.isArray(data?.items) ? data.items : [];
			const map: Record<string, string> = {};
			for (const mp of items) {
				const gpuModel: string | undefined = mp?.status?.capacity?.gpuModel;
				if (!gpuModel) continue;
				// Strip the vendor prefix for a compact label ("NVIDIA GeForce RTX 5090" -> "RTX 5090").
				const display = gpuModel.replace(/^NVIDIA GeForce /, '').trim();
				const name: string | undefined = mp?.metadata?.name;
				if (name) map[name] = display;
				const endpoint: string | undefined = mp?.spec?.endpoint;
				if (endpoint) {
					// Endpoint host "ollama.ollama-rig0" -> namespace component "ollama-rig0".
					const host = endpoint.replace(/^[a-z]+:\/\//, '').split('/')[0].split(':')[0];
					const parts = host.split('.');
					const ns = parts.length >= 2 ? parts[1] : parts[0];
					if (ns) map[ns] = display;
				}
			}
			gpuDisplayMap = map;
		} catch (e) {
			console.error('Failed to load GPU labels:', e);
		}
	}

	async function loadChannels() {
		try {
			const res = await fetch(`${base}/api/kubemoot/channels`);
			const data = await res.json();
			if (data.channels && data.channels.length > 0) {
				channels = data.channels;
				// Default to first channel for new discussions
				newChannel = channels[0].name;
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

	function handleGlobalKeydown(event: KeyboardEvent) {
		// Ignore when typing in input fields
		const tag = (event.target as HTMLElement)?.tagName;
		if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;

		if (event.key === 'Delete' && selectedThread && !deleteConfirm) {
			deleteConfirm = true;
		} else if (deleteConfirm && (event.key === 'y' || event.key === 'Y')) {
			deleteThread();
		} else if (deleteConfirm && (event.key === 'n' || event.key === 'N' || event.key === 'Escape')) {
			deleteConfirm = false;
		}
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
	// it only writes when the value actually changes — no infinite loop.
	$effect(() => {
		if (selectedThreadId && !visibleThreads.some((t) => t.threadId === selectedThreadId)) {
			selectedThreadId = visibleThreads[0]?.threadId ?? null;
		}
	});

	onMount(() => {
		initDiscussions();
		loadChannels();
		loadGpuLabels();
		loadPinnedThreads();
		fetch(`${base}/api/namespaces`).then(r => r.json()).then(d => { crews = Array.isArray(d.crews) ? d.crews : []; }).catch(() => {});
		fetch(`${base}/api/version`).then(r => r.json()).then(d => { dashboardVersion = d.version || 'dev'; }).catch(() => {});
		// Keyboard shortcuts for delete confirmation
		window.addEventListener('keydown', handleGlobalKeydown);
		// Auto-select first thread once history loads (if nothing selected)
		const unsub = sortedThreads.subscribe((sorted) => {
			if (sorted.length > 0 && !selectedThreadId) {
				selectedThreadId = sorted[0].threadId;
			}
		});
		return () => {
			unsub();
			window.removeEventListener('keydown', handleGlobalKeydown);
		};
	});
</script>

<div class="discussions-page">
	<div class="page-header">
		<h1>Agent Discussions</h1>
		<div class="header-actions">
			<button class="new-btn" onclick={() => (showNewForm = !showNewForm)}>
				{showNewForm ? 'Cancel' : '+ New Discussion'}
			</button>
			<div class="connection-status" class:connected={$discussionsConnected}>
				<span class="status-dot"></span>
				{$discussionsConnected ? 'Connected' : 'Disconnected'}
			</div>
		</div>
	</div>

	{#if showNewForm}
		<div class="new-discussion-form">
			<select bind:value={newChannel} class="channel-select">
				{#each channels as ch}
					<option value={ch.name}>{ch.name}</option>
				{/each}
			</select>
			<input
				type="text"
				bind:value={newQuery}
				placeholder="Ask a question to start a discussion..."
				class="new-query-input"
				onkeydown={handleNewKeydown}
				disabled={sending}
			/>
			<button class="send-btn" onclick={startNewDiscussion} disabled={sending || !newQuery.trim() || !selectedScope}>
				{sending ? 'Sending...' : 'Send'}
			</button>
			{#if !selectedScope}
				<span class="scope-hint">Pick a crew in the top bar to start a discussion.</span>
			{/if}
		</div>
	{/if}

	<div class="discussions-layout">
		<div class="thread-list">
			{#if !$historyLoaded && $sortedThreads.length === 0}
				<div class="empty-state">Loading discussion history...</div>
			{:else if visibleThreads.length === 0}
				<div class="empty-state">
					{#if selectedCrew}
						No discussions for crew "{selectedCrew}". Pick "All crews" to see all.
					{:else}
						No discussions yet. Click "+ New Discussion" to start one.
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
								<span class="crew-badge" title={thread.namespace ? `namespace ${thread.namespace}` : undefined}>{$namespace === '' && thread.namespace ? `${thread.namespace}/` : ''}{thread.crew}</span>
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
							<span>{formatTime(thread.startedAt)}</span>
						</div>
					</button>
					<button
						class="del-thread"
						title="Remove this discussion (deletes from the timeline and purges its messages)"
						aria-label="Remove discussion"
						onclick={(e) => removeDiscussion(thread.threadId, e)}
					>✕</button>
					</div>
				{/each}
			{/if}
		</div>

		<div class="thread-detail">
			{#if selectedThread}
				<div class="thread-detail-header">
					{#if selectedThread.crew}
						<span class="crew-badge">{selectedThread.crew}</span>
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
						<button class="action-btn" class:active-toggle={showSpanGraph} onclick={() => { showSpanGraph = !showSpanGraph; }} title="Toggle span graph">
							<svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
								<rect x="1" y="2" width="10" height="3" rx="1" fill="currentColor" opacity="0.8" />
								<rect x="1" y="7" width="14" height="3" rx="1" fill="currentColor" opacity="0.6" />
								<rect x="1" y="12" width="7" height="3" rx="1" fill="currentColor" opacity="0.4" />
							</svg>
						</button>
						<button class="action-btn" onclick={copyThread} title="Copy thread to clipboard">
							{copiedThread ? '✓' : '📋'}
						</button>
						<button
							class="action-btn"
							class:active-toggle={$pinnedThreadIds.has(selectedThread.threadId)}
							onclick={togglePin}
							title={$pinnedThreadIds.has(selectedThread.threadId) ? 'Unpin thread' : 'Pin thread (protects from delete)'}
						>
							{$pinnedThreadIds.has(selectedThread.threadId) ? '📌' : '📍'}
						</button>
						{#if selectedThread.status === 'open'}
							{#if isThreadPaused(selectedThread)}
								<button class="action-btn" onclick={resumeThread} disabled={sending} title="Resume paused discussion">
									▶️
								</button>
							{:else}
								<button class="action-btn" onclick={pauseThread} disabled={sending} title="Pause discussion — suspends settle timer">
									⏸️
								</button>
							{/if}
							<button class="action-btn" onclick={stopThread} disabled={sending} title="Stop discussion now — coordinator synthesizes with current signals">
								⏹️
							</button>
						{/if}
						{#if selectedThread.status !== 'open'}
							<button class="action-btn" onclick={replayThread} disabled={sending} title="Replay this question as a new discussion">
								🔁
							</button>
						{/if}
						{#if !deleteConfirm}
							<button class="action-btn danger" onclick={() => { deleteConfirm = true; }} title="Delete thread (Del)">
								🗑️
							</button>
						{:else}
							<span class="delete-prompt">Delete?</span>
							<button class="action-btn-text yes" onclick={deleteThread}>Yes</button>
							<button class="action-btn-text no" onclick={() => { deleteConfirm = false; }}>No</button>
						{/if}
					</div>
				</div>
				<div class="thread-query-full" title={selectedThread.threadId}>
					<span>{selectedThread.userQuery}</span>
					<button class="thread-id-copy" onclick={copyThreadId} title="Copy thread ID">
						{copiedThreadId ? '✓' : '#'}
					</button>
				</div>

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
					{#each selectedThread.messages.filter((m) => $showStandAsides || (m.messageType !== 'stand_aside' && m.messageType !== 'decline')) as msg, msgIdx}
						<div
							class="message-card {msg.messageType}"
							class:synthesis={msg.messageType === 'synthesis'}
							class:decline={msg.messageType === 'decline' || msg.messageType === 'stand_aside'}
							class:advisory={msg.messageType === 'advisory'}
							class:advisory_ready={msg.messageType === 'advisory_ready'}
							class:review_ready={msg.messageType === 'review_ready'}
							class:concern={msg.messageType === 'concern'}
							class:block={msg.messageType === 'block'}
							class:proposal={msg.messageType === 'proposal'}
							class:human={msg.agentName === 'human'}
						>
							<div class="message-header">
								<span class="agent-name" class:human-name={msg.agentName === 'human'}>
									{msg.agentName === 'human' ? 'You' : msg.agentName.replace('homelab-', '')}
								</span>
								<span class="message-type-badge {msg.messageType}">
									{messageTypeLabels[msg.messageType] || msg.messageType}
								</span>
								{#if (msg.messageType === 'decline' || msg.messageType === 'stand_aside') && msg.metadata?.reason}
									<span class="decline-reason">({msg.metadata.reason})</span>
								{/if}
								<span class="message-time">{formatTime(msg.timestamp)}</span>
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
							{#if msg.metadata?.inputTokens || msg.metadata?.outputTokens}
								<span class="token-badge" title="Input/output tokens">
									{msg.metadata.inputTokens ?? 0}↓ {msg.metadata.outputTokens ?? 0}↑
								</span>
							{/if}
							{#if msg.messageType === 'thread_close' && msg.metadata?.totalTokens}
								<div class="token-summary">
									Tokens: advisory {msg.metadata.totalTokens.advisory?.in ?? 0}↓/{msg.metadata.totalTokens.advisory?.out ?? 0}↑
									| triage {msg.metadata.totalTokens.triage?.in ?? 0}↓/{msg.metadata.totalTokens.triage?.out ?? 0}↑
									| synthesis {msg.metadata.totalTokens.synthesis?.in ?? 0}↓/{msg.metadata.totalTokens.synthesis?.out ?? 0}↑
								</div>
							{/if}
						</div>
					{/each}
				</div>

				<div class="reply-bar">
					<input
						type="text"
						bind:value={replyText}
						placeholder="Add to this discussion..."
						class="reply-input"
						onkeydown={handleReplyKeydown}
						disabled={sending}
					/>
					<button class="send-btn" onclick={sendReply} disabled={sending || !replyText.trim()}>
						Send
					</button>
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

	.new-btn {
		padding: 0.375rem 0.75rem;
		border-radius: 0.375rem;
		border: 1px solid var(--color-primary);
		background: transparent;
		color: var(--color-primary);
		font-size: 0.8rem;
		font-weight: 500;
		cursor: pointer;
		font-family: inherit;
		transition: all 0.15s;
	}

	.new-btn:hover {
		background: var(--color-primary);
		color: white;
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

	.scope-hint {
		align-self: center;
		font-size: 0.8rem;
		color: var(--text-muted, #9ca3af);
	}

	.new-discussion-form {
		display: flex;
		gap: 0.5rem;
		padding: 0.75rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		flex-shrink: 0;
	}

	.channel-select {
		padding: 0.5rem;
		border-radius: 0.375rem;
		border: 1px solid var(--color-border);
		background: var(--color-bg);
		color: var(--color-text);
		font-size: 0.8rem;
		font-family: inherit;
	}

	.new-query-input {
		flex: 1;
		padding: 0.5rem 0.75rem;
		border-radius: 0.375rem;
		border: 1px solid var(--color-border);
		background: var(--color-bg);
		color: var(--color-text);
		font-size: 0.8rem;
		font-family: inherit;
	}

	.new-query-input:focus, .reply-input:focus {
		outline: none;
		border-color: var(--color-primary);
	}

	.send-btn {
		padding: 0.5rem 1rem;
		border-radius: 0.375rem;
		border: none;
		background: var(--color-primary);
		color: white;
		font-size: 0.8rem;
		font-weight: 500;
		cursor: pointer;
		font-family: inherit;
		transition: opacity 0.15s;
	}

	.send-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.send-btn:hover:not(:disabled) {
		opacity: 0.9;
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

	/* Wrapper holds the clickable card + a hover-revealed remove button (buttons
	   can't nest, so the ✕ is a sibling positioned over the card's corner). */
	.thread-row {
		position: relative;
	}
	.del-thread {
		position: absolute;
		top: 0.4rem;
		right: 0.4rem;
		background: none;
		border: none;
		color: var(--color-text-muted, #888);
		cursor: pointer;
		font-size: 0.85rem;
		line-height: 1;
		padding: 0.1rem 0.3rem;
		border-radius: 4px;
		opacity: 0;
		transition: opacity 0.12s;
	}
	.thread-row:hover .del-thread {
		opacity: 1;
	}
	.del-thread:hover {
		color: #e5534b;
		background: var(--color-bg-secondary);
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

	.token-badge {
		display: inline-block;
		font-size: 0.6rem;
		color: var(--color-text-muted);
		margin-top: 0.25rem;
		font-family: monospace;
	}

	.token-summary {
		font-size: 0.6rem;
		color: var(--color-text-muted);
		margin-top: 0.375rem;
		font-family: monospace;
		padding: 0.25rem 0.5rem;
		background: rgba(107, 114, 128, 0.1);
		border-radius: 0.25rem;
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

	.action-btn.danger:hover {
		background-color: rgba(239, 68, 68, 0.1);
	}

	.delete-prompt {
		font-size: 0.75rem;
		font-weight: 600;
		color: #f87171;
	}

	.action-btn-text {
		background: none;
		border: 1px solid;
		padding: 0.125rem 0.5rem;
		border-radius: 0.25rem;
		font-size: 0.7rem;
		font-weight: 600;
		cursor: pointer;
		font-family: inherit;
	}

	.action-btn-text.yes {
		color: #10b981;
		border-color: #10b981;
	}

	.action-btn-text.yes:hover {
		background-color: rgba(16, 185, 129, 0.15);
	}

	.action-btn-text.no {
		color: #f87171;
		border-color: #f87171;
	}

	.action-btn-text.no:hover {
		background-color: rgba(239, 68, 68, 0.1);
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

	.reply-input {
		flex: 1;
		padding: 0.5rem 0.75rem;
		border-radius: 0.375rem;
		border: 1px solid var(--color-border);
		background: var(--color-bg);
		color: var(--color-text);
		font-size: 0.8rem;
		font-family: inherit;
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
