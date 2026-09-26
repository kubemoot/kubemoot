<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { base } from '$app/paths';

	interface NatsMessage {
		subject: string;
		data: string;
		timestamp: Date;
	}

	let messages = $state<NatsMessage[]>([]);
	let connectionStatus = $state<'disconnected' | 'connecting' | 'connected' | 'error'>(
		'disconnected'
	);
	let connectionError = $state<string | null>(null);
	let selectedChannel = $state('kubemoot.>');
	let customSubject = $state('');
	let composeText = $state('');
	let publishSubject = $state('kubemoot.chat.admin.general');
	let messagesContainer: HTMLDivElement;

	const channels = [
		{ subject: 'kubemoot.>', label: 'All Kubemoot Events' },
		{ subject: 'kubemoot.chronicle.>', label: 'Chronicle Events' },
		{ subject: 'kubemoot.quality.>', label: 'Quality Verdicts' },
		{ subject: 'kubemoot.chat.>', label: 'Chat Messages' },
		{ subject: 'kubemoot.discuss.>', label: 'Discussion Threads' },
		{ subject: 'kubemoot.operator.>', label: 'Operator Events' },
		{ subject: 'kubemoot.gateway.>', label: 'Gateway Feedback' }
	];

	let eventSource: EventSource | null = null;
	let reconnectAttempts = 0;
	let reconnectTimeout: ReturnType<typeof setTimeout> | null = null;

	function connect() {
		connectionStatus = 'connecting';
		connectionError = null;

		// Clean up previous connection
		if (reconnectTimeout) {
			clearTimeout(reconnectTimeout);
			reconnectTimeout = null;
		}
		if (eventSource) {
			eventSource.close();
			eventSource = null;
		}

		// Connect via SSE through the server-side proxy
		const url = `${base}/api/nats/subscribe?subject=${encodeURIComponent(selectedChannel)}`;
		eventSource = new EventSource(url);

		eventSource.onmessage = (event) => {
			try {
				const payload = JSON.parse(event.data);

				if (payload.type === 'connected') {
					connectionStatus = 'connected';
					reconnectAttempts = 0;
					return;
				}

				if (payload.type === 'error') {
					connectionStatus = 'error';
					connectionError = payload.error;
					return;
				}

				if (payload.type === 'message') {
					messages = [
						...messages,
						{
							subject: payload.subject,
							data: payload.data,
							timestamp: new Date(payload.timestamp)
						}
					];
					// Auto-scroll
					requestAnimationFrame(() => {
						if (messagesContainer) {
							messagesContainer.scrollTop = messagesContainer.scrollHeight;
						}
					});
				}
			} catch {
				// Ignore parse errors
			}
		};

		eventSource.onerror = () => {
			eventSource?.close();
			eventSource = null;

			// Auto-retry with exponential backoff (1s, 2s, 4s, 8s, 16s max)
			if (reconnectAttempts < 5) {
				connectionStatus = 'connecting';
				connectionError = `Reconnecting (attempt ${reconnectAttempts + 1})...`;
				const delay = Math.min(1000 * Math.pow(2, reconnectAttempts), 16000);
				reconnectTimeout = setTimeout(() => {
					reconnectAttempts++;
					connect();
				}, delay);
			} else {
				connectionStatus = 'error';
				connectionError = 'Connection lost after 5 retries';
				reconnectAttempts = 0;
			}
		};
	}

	function changeChannel(subject: string) {
		selectedChannel = subject;
		messages = [];
		connect();
	}

	function subscribeCustom() {
		if (customSubject.trim()) {
			selectedChannel = customSubject.trim();
			messages = [];
			connect();
			customSubject = '';
		}
	}

	async function publishMessage() {
		if (!composeText.trim()) return;

		const payload = JSON.stringify({
			from: 'admin',
			message: composeText.trim(),
			timestamp: new Date().toISOString()
		});

		try {
			const res = await fetch(`${base}/api/nats/publish`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ subject: publishSubject, data: payload })
			});

			if (!res.ok) {
				const err = await res.json();
				console.error('Publish failed:', err.error);
			}

			composeText = '';
		} catch (e) {
			console.error('Publish error:', e);
		}
	}

	function formatData(data: string): string {
		try {
			return JSON.stringify(JSON.parse(data), null, 2);
		} catch {
			return data;
		}
	}

	function clearMessages() {
		messages = [];
	}

	onMount(() => {
		connect();
	});

	onDestroy(() => {
		if (reconnectTimeout) {
			clearTimeout(reconnectTimeout);
			reconnectTimeout = null;
		}
		if (eventSource) {
			eventSource.close();
			eventSource = null;
		}
	});
</script>

<div class="messages-page">
	<div class="sidebar-panel">
		<div class="panel-header">
			<h2>Channels</h2>
			<div
				class="connection-status"
				class:connected={connectionStatus === 'connected'}
				class:error={connectionStatus === 'error'}
			>
				<span class="status-dot"></span>
				{connectionStatus}
			</div>
		</div>

		{#if connectionError}
			<div class="connection-error">{connectionError}</div>
		{/if}

		<div class="channel-list">
			{#each channels as channel}
				<button
					class="channel-btn"
					class:active={selectedChannel === channel.subject}
					onclick={() => changeChannel(channel.subject)}
				>
					{channel.label}
					<span class="channel-subject">{channel.subject}</span>
				</button>
			{/each}
		</div>

		<div class="custom-sub">
			<input
				type="text"
				bind:value={customSubject}
				placeholder="Custom subject..."
				onkeydown={(e) => e.key === 'Enter' && subscribeCustom()}
			/>
			<button onclick={subscribeCustom}>Sub</button>
		</div>

		{#if connectionStatus === 'disconnected' || connectionStatus === 'error'}
			<button class="reconnect-btn" onclick={connect}>Reconnect</button>
		{/if}
	</div>

	<div class="main-panel">
		<div class="main-header">
			<h1>Messages</h1>
			<div class="header-controls">
				<span class="msg-count">{messages.length} messages</span>
				<span class="current-sub">Subscribed: <code>{selectedChannel}</code></span>
				<button class="clear-btn" onclick={clearMessages}>Clear</button>
			</div>
		</div>

		<div class="messages-timeline" bind:this={messagesContainer}>
			{#if messages.length === 0}
				<div class="empty-state">
					{#if connectionStatus === 'connected'}
						<p>Listening on <code>{selectedChannel}</code>...</p>
						<p class="hint">Messages will appear here in real-time.</p>
					{:else}
						<p>Not connected to NATS.</p>
					{/if}
				</div>
			{:else}
				{#each messages as msg}
					<div class="message">
						<div class="message-header">
							<span class="message-subject">{msg.subject}</span>
							<span class="message-time">{msg.timestamp.toLocaleTimeString()}</span>
						</div>
						<pre class="message-data">{formatData(msg.data)}</pre>
					</div>
				{/each}
			{/if}
		</div>

		<div class="compose-bar">
			<div class="compose-subject">
				<label for="pub-subject">Publish to:</label>
				<input id="pub-subject" type="text" bind:value={publishSubject} />
			</div>
			<div class="compose-input">
				<input
					type="text"
					bind:value={composeText}
					placeholder="Type a message..."
					onkeydown={(e) => e.key === 'Enter' && publishMessage()}
					disabled={connectionStatus !== 'connected'}
				/>
				<button
					onclick={publishMessage}
					disabled={connectionStatus !== 'connected' || !composeText.trim()}
				>
					Send
				</button>
			</div>
		</div>
	</div>
</div>

<style>
	.messages-page {
		display: flex;
		height: 100%;
		overflow: hidden;
	}

	.sidebar-panel {
		width: 240px;
		border-right: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		padding: 1rem;
		flex-shrink: 0;
		overflow-y: auto;
	}

	.panel-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 1rem;
	}

	.panel-header h2 {
		font-size: 0.9rem;
		font-weight: 600;
		margin: 0;
	}

	.connection-status {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.7rem;
		color: var(--color-text-muted);
	}

	.status-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--color-text-muted);
	}

	.connection-status.connected .status-dot {
		background: #22c55e;
	}
	.connection-status.error .status-dot {
		background: #ef4444;
	}

	.connection-error {
		font-size: 0.75rem;
		color: #ef4444;
		margin-bottom: 0.5rem;
		padding: 0.5rem;
		background: rgba(239, 68, 68, 0.1);
		border-radius: 4px;
	}

	.channel-list {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		flex: 1;
	}

	.channel-btn {
		display: flex;
		flex-direction: column;
		gap: 0.1rem;
		padding: 0.5rem 0.75rem;
		border: none;
		border-radius: 6px;
		background: none;
		color: var(--color-text-muted);
		cursor: pointer;
		text-align: left;
		font-size: 0.8rem;
	}

	.channel-btn:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text);
	}
	.channel-btn.active {
		background: var(--color-primary);
		color: white;
	}

	.channel-subject {
		font-size: 0.65rem;
		font-family: monospace;
		opacity: 0.7;
	}

	.custom-sub {
		display: flex;
		gap: 0.25rem;
		margin-top: 0.75rem;
	}

	.custom-sub input {
		flex: 1;
		padding: 0.35rem 0.5rem;
		background: var(--color-bg);
		color: var(--color-text);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font-size: 0.75rem;
	}

	.custom-sub button {
		padding: 0.35rem 0.5rem;
		background: var(--color-bg-secondary);
		color: var(--color-text);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font-size: 0.75rem;
		cursor: pointer;
	}

	.reconnect-btn {
		margin-top: 0.5rem;
		padding: 0.4rem;
		background: var(--color-primary);
		color: white;
		border: none;
		border-radius: 4px;
		font-size: 0.8rem;
		cursor: pointer;
	}

	.main-panel {
		flex: 1;
		display: flex;
		flex-direction: column;
		overflow: hidden;
	}

	.main-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 1rem 1.5rem;
		border-bottom: 1px solid var(--color-border);
	}

	.main-header h1 {
		font-size: 1.1rem;
		font-weight: 600;
		margin: 0;
	}

	.header-controls {
		display: flex;
		align-items: center;
		gap: 1rem;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.current-sub code {
		font-size: 0.7rem;
		background: var(--color-bg-secondary);
		padding: 0.15rem 0.35rem;
		border-radius: 3px;
	}

	.clear-btn {
		padding: 0.25rem 0.5rem;
		background: var(--color-bg-secondary);
		color: var(--color-text);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font-size: 0.7rem;
		cursor: pointer;
	}

	.messages-timeline {
		flex: 1;
		overflow-y: auto;
		padding: 1rem 1.5rem;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.empty-state {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		height: 100%;
		color: var(--color-text-muted);
		font-size: 0.9rem;
	}

	.empty-state code {
		background: var(--color-bg-secondary);
		padding: 0.1rem 0.3rem;
		border-radius: 3px;
		font-size: 0.8rem;
	}

	.hint {
		font-size: 0.75rem;
		margin-top: 0.25rem;
	}

	.message {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		padding: 0.5rem 0.75rem;
	}

	.message-header {
		display: flex;
		justify-content: space-between;
		margin-bottom: 0.35rem;
	}

	.message-subject {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-primary);
		font-family: monospace;
	}

	.message-time {
		font-size: 0.7rem;
		color: var(--color-text-muted);
	}

	.message-data {
		font-size: 0.75rem;
		font-family: monospace;
		background: var(--color-bg);
		padding: 0.5rem;
		border-radius: 4px;
		margin: 0;
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 200px;
		overflow-y: auto;
	}

	.compose-bar {
		border-top: 1px solid var(--color-border);
		padding: 0.75rem 1.5rem;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.compose-subject {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.75rem;
	}

	.compose-subject label {
		color: var(--color-text-muted);
		white-space: nowrap;
	}

	.compose-subject input {
		flex: 1;
		padding: 0.3rem 0.5rem;
		background: var(--color-bg);
		color: var(--color-text);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font-size: 0.75rem;
		font-family: monospace;
	}

	.compose-input {
		display: flex;
		gap: 0.5rem;
	}

	.compose-input input {
		flex: 1;
		padding: 0.5rem 0.75rem;
		background: var(--color-bg);
		color: var(--color-text);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		font-size: 0.85rem;
	}

	.compose-input button {
		padding: 0.5rem 1rem;
		background: var(--color-primary);
		color: white;
		border: none;
		border-radius: 6px;
		font-size: 0.85rem;
		cursor: pointer;
	}

	.compose-input button:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
</style>
