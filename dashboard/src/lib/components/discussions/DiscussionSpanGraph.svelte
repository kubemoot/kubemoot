<script lang="ts">
	import type { DiscussionMessage } from '$types/kubemoot.js';
	import { showStandAsides } from '$lib/stores';
	import { buildAgentSpans, type AgentSpan } from '$lib/discussion-spans';

	let {
		messages = [],
		threadId = '',
		userQuery = '',
		version = '',
		crew = '',
		gpuDisplayMap = {}
	}: {
		messages: DiscussionMessage[];
		threadId?: string;
		userQuery?: string;
		version?: string;
		crew?: string;
		// Maps a published provider id (ModelProvider CR name or its endpoint
		// namespace, e.g. "ollama-gpu" / "ollama-rig1") to the operator-discovered
		// GPU model (DCGM, e.g. "RTX 5090"). The agent publishes a stable provider
		// id in span metadata; this resolves it to the human GPU name for display
		// only. Empty map (or unknown id) falls back to the raw provider id.
		gpuDisplayMap?: Record<string, string>;
	} = $props();

	// Resolve a published provider-id gpuLabel to the discovered GPU model name
	// for display. Falls back to the raw label when the provider is unknown.
	function gpuName(label: string | undefined): string {
		if (!label) return '';
		return gpuDisplayMap[label] ?? label;
	}

	let copiedImage = $state(false);

	const LABEL_WIDTH = 160;
	const ROW_HEIGHT = 36;
	const BAR_HEIGHT = 20;
	const PADDING_TOP = 30;
	const PADDING_RIGHT = 60;
	const PADDING_LEFT = 8;

	// GPU colors assigned dynamically from message data - no hardcoded GPU models
	const gpuColorPalette = ['#3b82f6', '#f97316', '#a78bfa', '#34d399', '#f472b6', '#facc15', '#22d3ee', '#fb923c'];

	const legendItems = [
		{ label: 'Agree', color: '#34d399', tooltip: 'Agent ran tools and found relevant data to contribute' },
		{ label: 'Advisory', color: '#c084fc', tooltip: 'Coordinator identified relevant technologies and context for the query' },
		{ label: 'Concern', color: '#fbbf24', tooltip: 'Agent found a potential risk or caveat worth highlighting' },
		{ label: 'Block', color: '#f87171', tooltip: 'Agent raised a serious objection - synthesis is halted' },
		{ label: 'Stand Aside', color: '#6b7280', tooltip: 'Query is outside this agent\'s domain - no contribution' },
		{ label: 'Proposal', color: '#60a5fa', tooltip: 'Onboarding agent proposed a new tool to fill a capability gap' },
		{ label: 'Synthesis', color: '#fbbf24', tooltip: 'Coordinator synthesized all agent contributions into a final answer' },
		{ label: 'Triaging', color: '#38bdf8', tooltip: 'Agent is queued on the triage GPU deciding whether to contribute' },
		{ label: 'Evaluating', color: '#818cf8', tooltip: 'Agent passed triage and is running full tool-calling inference' },
		{ label: 'Heartbeat', color: '#64748b', tooltip: 'Agent is alive - queued for or running LLM inference' },
		{ label: 'Waking', color: '#fb923c', tooltip: 'Agent is starting from scale-to-zero - cold start in progress' },
		{ label: 'Ready', color: '#22d3ee', tooltip: 'Agent finished cold start and is ready for triage' },
		{ label: 'Inference', color: '#9ca3af', type: 'bar-solid' as const, tooltip: 'Saturated bar portion - actual LLM inference time' },
		{ label: 'Overhead', color: '#9ca3af', type: 'bar-faded' as const, tooltip: 'Faded bar portion - GPU queue wait and triage time before inference' },
		{ label: 'Queue Depth', color: '#ef4444', type: 'queue' as const, tooltip: 'Number of other agents running inference on the same GPU concurrently' }
	];

	// Derive GPU color map from actual message data
	const gpuColors = $derived.by(() => {
		const map: Record<string, string> = {};
		let idx = 0;
		for (const msg of messages) {
			const label = msg.metadata?.gpuLabel;
			if (label && !(label in map)) {
				map[label] = gpuColorPalette[idx % gpuColorPalette.length];
				idx++;
			}
		}
		return map;
	});

	let containerEl = $state<HTMLDivElement | null>(null);
	let containerWidth = $state(600);
	let hoveredAgent = $state<string | null>(null);

	const spans = $derived(buildAgentSpans(messages));

	// Hide agents that immediately stand_aside with no inference. Driven by the
	// global "Show stand-asides" preference (Config page) so the graph matches
	// the Discussions/Fitness views - one setting, no per-view toggle.
	const hideNoWorkAgents = $derived(!$showStandAsides);

	function isNoWork(s: AgentSpan): boolean {
		return !s.isHuman && s.inferenceMs === 0 && s.primarySignal === 'stand_aside';
	}

	const hiddenNoWorkCount = $derived(spans.filter(isNoWork).length);
	const visibleSpans = $derived(
		hideNoWorkAgents ? spans.filter((s) => !isNoWork(s)) : spans
	);

	const maxMs = $derived(Math.max(...visibleSpans.map((s) => s.endMs), 1000));

	const ticks = $derived.by(() => {
		const maxSec = maxMs / 1000;
		// Target ~8-12 ticks regardless of span duration
		const nice = [1, 2, 5, 10, 15, 20, 30, 60, 120, 300, 600];
		const rawInterval = maxSec / 8;
		let interval = nice.find(n => n >= rawInterval) ?? Math.ceil(rawInterval / 60) * 60;

		const result: number[] = [];
		for (let t = 0; t <= maxSec + interval; t += interval) {
			result.push(t);
		}
		return result;
	});

	const chartWidth = $derived(containerWidth - LABEL_WIDTH - PADDING_RIGHT - PADDING_LEFT);
	const svgHeight = $derived(PADDING_TOP + visibleSpans.length * ROW_HEIGHT + 8);

	function msToX(ms: number): number {
		const maxTick = ticks[ticks.length - 1] * 1000;
		return LABEL_WIDTH + PADDING_LEFT + (ms / maxTick) * chartWidth;
	}

	function formatDuration(ms: number): string {
		if (ms < 1000) return `${ms}ms`;
		return `${(ms / 1000).toFixed(1)}s`;
	}

	$effect(() => {
		if (!containerEl) return;
		const observer = new ResizeObserver((entries) => {
			for (const entry of entries) {
				containerWidth = entry.contentRect.width;
			}
		});
		observer.observe(containerEl);
		return () => observer.disconnect();
	});

	/** Word-wrap text into lines that fit within maxWidth (approx charWidth per char). */
	function wrapText(text: string, maxWidth: number, charWidth: number): string[] {
		const maxChars = Math.floor(maxWidth / charWidth);
		const words = text.split(' ');
		const lines: string[] = [];
		let current = '';
		for (const word of words) {
			if (current && (current + ' ' + word).length > maxChars) {
				lines.push(current);
				current = word;
			} else {
				current = current ? current + ' ' + word : word;
			}
		}
		if (current) lines.push(current);
		return lines;
	}

	/** Title block of the copied image: heading, crew, wrapped thread line, version and time. */
	function graphTitleLines(maxWidth: number, charWidth: number): string[] {
		const now = new Date().toLocaleString();
		const rawQuery = userQuery || 'Unknown';
		const queryText = rawQuery.length > 128 ? rawQuery.substring(0, 128) + '...' : rawQuery;
		const threadLine = `Thread id: ${threadId || 'unknown'} | ${queryText}`;
		const metaLine = `v${version || 'dev'}  ${now}`;
		return [
			'Kubemoot Discussion Agents',
			...(crew ? [`Crew: ${crew}`] : []),
			...wrapText(threadLine, maxWidth, charWidth),
			metaLine
		];
	}

	async function copyGraphImage() {
		if (!containerEl) return;
		const svgEl = containerEl.querySelector('svg');
		if (!svgEl) return;

		const LINE_HEIGHT = 18;
		const LEGEND_HEIGHT = 40;
		const PADDING = 16;
		const CHAR_WIDTH = 7.8; // approx monospace char width at 13px
		const BG_COLOR = '#0f172a';

		const clone = svgEl.cloneNode(true) as SVGSVGElement;
		const origWidth = svgEl.clientWidth || containerWidth;
		const origHeight = svgEl.clientHeight || svgHeight;
		const contentWidth = origWidth + PADDING * 2;

		const titleLines = graphTitleLines(contentWidth - PADDING * 2, CHAR_WIDTH);

		const TITLE_HEIGHT = PADDING + titleLines.length * LINE_HEIGHT + 8;

		// Create wrapper SVG
		const totalHeight = TITLE_HEIGHT + origHeight + LEGEND_HEIGHT;
		const wrapper = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
		wrapper.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
		wrapper.setAttribute('width', String(contentWidth));
		wrapper.setAttribute('height', String(totalHeight));

		// Background
		const bg = document.createElementNS('http://www.w3.org/2000/svg', 'rect');
		bg.setAttribute('width', '100%');
		bg.setAttribute('height', '100%');
		bg.setAttribute('fill', BG_COLOR);
		wrapper.appendChild(bg);

		// Title lines
		titleLines.forEach((line, i) => {
			const text = document.createElementNS('http://www.w3.org/2000/svg', 'text');
			text.setAttribute('x', String(PADDING));
			text.setAttribute('y', String(PADDING + (i + 1) * LINE_HEIGHT));
			text.setAttribute('fill', i === 0 ? '#e2e8f0' : '#94a3b8');
			text.setAttribute('font-size', i === 0 ? '14' : '12');
			text.setAttribute('font-weight', i === 0 ? '600' : '400');
			text.setAttribute('font-family', 'ui-monospace, monospace');
			text.textContent = line;
			wrapper.appendChild(text);
		});

		// Position the chart SVG below the title
		clone.setAttribute('x', String(PADDING));
		clone.setAttribute('y', String(TITLE_HEIGHT));
		clone.setAttribute('width', String(origWidth));
		clone.setAttribute('height', String(origHeight));
		// Remove any foreignObject elements (tool badges on hover) - they break serialization
		clone.querySelectorAll('foreignObject').forEach((fo) => fo.remove());
		wrapper.appendChild(clone);

		// Legend as text line
		const legendY = TITLE_HEIGHT + origHeight + 20;
		const legendEntries = legendItems;
		let legendX = PADDING;
		for (const item of legendEntries) {
			const swatch = document.createElementNS('http://www.w3.org/2000/svg', 'rect');
			swatch.setAttribute('x', String(legendX));
			swatch.setAttribute('y', String(legendY - 6));
			swatch.setAttribute('width', '10');
			swatch.setAttribute('height', '10');
			swatch.setAttribute('rx', '2');
			swatch.setAttribute('fill', item.color);
			swatch.setAttribute('opacity', item.type === 'bar-faded' ? '0.25' : '0.75');
			wrapper.appendChild(swatch);

			const label = document.createElementNS('http://www.w3.org/2000/svg', 'text');
			label.setAttribute('x', String(legendX + 14));
			label.setAttribute('y', String(legendY));
			label.setAttribute('fill', '#9ca3af');
			label.setAttribute('font-size', '10');
			label.setAttribute('font-family', 'ui-monospace, monospace');
			label.textContent = item.label;
			wrapper.appendChild(label);

			legendX += 14 + item.label.length * 6.5 + 16;
		}

		// Serialize and render to canvas
		const serializer = new XMLSerializer();
		const svgString = serializer.serializeToString(wrapper);
		const blob = new Blob([svgString], { type: 'image/svg+xml;charset=utf-8' });
		const url = URL.createObjectURL(blob);

		const img = new Image();
		img.onload = async () => {
			const scale = 2; // Retina quality
			const canvas = document.createElement('canvas');
			canvas.width = img.width * scale;
			canvas.height = img.height * scale;
			const ctx = canvas.getContext('2d')!;
			ctx.scale(scale, scale);
			ctx.drawImage(img, 0, 0);
			URL.revokeObjectURL(url);

			try {
				const canvasBlob = await new Promise<Blob>((resolve, reject) => {
					canvas.toBlob((b) => (b ? resolve(b) : reject(new Error('Canvas toBlob failed'))), 'image/png');
				});
				await navigator.clipboard.write([
					new ClipboardItem({ 'image/png': canvasBlob })
				]);
				copiedImage = true;
				setTimeout(() => { copiedImage = false; }, 2000);
			} catch (err) {
				console.error('Failed to copy image:', err);
			}
		};
		img.src = url;
	}
</script>

<div class="span-graph" bind:this={containerEl}>
	{#if hideNoWorkAgents && hiddenNoWorkCount > 0}
		<div class="graph-controls">
			<span class="hidden-count">{hiddenNoWorkCount} no-work agent{hiddenNoWorkCount === 1 ? '' : 's'} hidden</span>
		</div>
	{/if}
	<button
		class="copy-image-btn"
		class:copied={copiedImage}
		onclick={copyGraphImage}
		title="Copy agent graph image to clipboard"
	>
		{copiedImage ? '✓' : '📷'}
	</button>
	<svg width="100%" height={svgHeight} xmlns="http://www.w3.org/2000/svg">
		<!-- Time axis ticks -->
		{#each ticks as tick}
			{@const x = msToX(tick * 1000)}
			<line x1={x} y1={PADDING_TOP - 10} x2={x} y2={svgHeight} stroke="#374151" stroke-width="1" stroke-dasharray="4,4" />
			<text x={x} y={PADDING_TOP - 14} text-anchor="middle" fill="#9ca3af" font-size="10" font-family="ui-monospace, monospace">{tick >= 120 ? `${(tick / 60).toFixed(0)}m` : `${tick}s`}</text>
		{/each}

		<!-- Agent rows -->
		{#each visibleSpans as span, i}
			{@const y = PADDING_TOP + i * ROW_HEIGHT}
			{@const barY = y + (ROW_HEIGHT - BAR_HEIGHT) / 2}
			{@const barStartX = msToX(span.startMs)}
			{@const barEndX = msToX(span.endMs)}
			{@const barWidth = Math.max(barEndX - barStartX, 4)}
			{@const isHovered = hoveredAgent === span.agent}
			{@const spanDuration = span.endMs - span.startMs}
			{@const hasInference = span.inferenceMs > 0 && spanDuration > 0}
			{@const overheadMs = hasInference ? Math.max(spanDuration - span.inferenceMs, 0) : 0}
			{@const overheadWidth = hasInference ? (overheadMs / Math.max(spanDuration, 1)) * barWidth : 0}
			{@const inferenceWidth = hasInference ? barWidth - overheadWidth : 0}
			{@const gpuColor = span.gpuLabel ? (gpuColors[span.gpuLabel] || '#6b7280') : span.color}
			{@const loadMs = hasInference ? Math.min(span.loadDurationMs, span.inferenceMs) : 0}
			{@const hasLoad = loadMs > 0 && inferenceWidth > 4}
			{@const loadWidth = hasLoad ? (loadMs / Math.max(span.inferenceMs, 1)) * inferenceWidth : 0}
			{@const evalWidth = hasInference ? inferenceWidth - loadWidth : 0}

			<!-- Row highlight on hover -->
			{#if isHovered}
				<rect x="0" y={y} width={containerWidth} height={ROW_HEIGHT} fill="rgba(255,255,255,0.03)" rx="4" />
			{/if}

			<!-- Agent label (right-aligned) -->
			<text
				x={span.gpuLabel ? LABEL_WIDTH - 48 : LABEL_WIDTH - 8}
				y={y + ROW_HEIGHT / 2}
				text-anchor="end"
				dominant-baseline="central"
				fill={isHovered ? '#f3f4f6' : '#d1d5db'}
				font-size="12"
				font-weight={isHovered ? '600' : '400'}
				font-family="ui-monospace, monospace"
			>{span.displayName}</text>

			<!-- GPU label badge -->
			{#if span.gpuLabel}
				{@const badgeColor = gpuColors[span.gpuLabel] || '#6b7280'}
				{@const badgeText = gpuName(span.gpuLabel).replace('RTX ', '')}
				{#if span.pickReason}
					<title>{span.pickReason}</title>
				{/if}
				<rect
					x={LABEL_WIDTH - 42}
					y={y + ROW_HEIGHT / 2 - 8}
					width="36"
					height="16"
					rx="3"
					fill={badgeColor}
					opacity="0.2"
				/>
				<text
					x={LABEL_WIDTH - 24}
					y={y + ROW_HEIGHT / 2}
					text-anchor="middle"
					dominant-baseline="central"
					fill={badgeColor}
					font-size="9"
					font-weight="600"
					font-family="ui-monospace, monospace"
				>{badgeText}</text>
			{/if}

			<!-- eslint-disable-next-line svelte/valid-compile -->
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<g
				onmouseenter={() => { hoveredAgent = span.agent; }}
				onmouseleave={() => { hoveredAgent = null; }}
				style="cursor: default;"
			>
				{#if span.pickReason}
					<title>{span.pickReason}</title>
				{/if}
				{#if span.isHuman}
					<!-- Human: point marker -->
					<circle cx={barStartX} cy={y + ROW_HEIGHT / 2} r="6" fill="#10b981" opacity="0.9" />
					<circle cx={barStartX} cy={y + ROW_HEIGHT / 2} r="3" fill="white" />
				{:else if hasInference}
					<!--
					Split bar: overhead (left, lighter) +
					           load prefix (pale GPU color, when load_duration > 0) +
					           eval (saturated GPU color)
					The load prefix is the time Ollama spent loading the model
					into VRAM, separate from token generation. Surfaces cold-
					start cost so a slow span doesn't look like "the LLM was
					slow" when it was actually "the model was cold".
					-->
					{#if overheadWidth > 1}
						<rect
							x={barStartX}
							y={barY}
							width={overheadWidth}
							height={BAR_HEIGHT}
							rx="4"
							fill={span.color}
							opacity={isHovered ? 0.4 : 0.25}
						/>
					{/if}
					{#if hasLoad}
						<rect
							x={barStartX + overheadWidth}
							y={barY}
							width={Math.max(loadWidth, 1)}
							height={BAR_HEIGHT}
							rx={overheadWidth > 1 ? 0 : 4}
							fill={gpuColor}
							opacity={isHovered ? 0.55 : 0.35}
						>
							<title>Model load: {formatDuration(loadMs)}</title>
						</rect>
					{/if}
					<rect
						x={barStartX + overheadWidth + loadWidth}
						y={barY}
						width={Math.max(evalWidth, 4)}
						height={BAR_HEIGHT}
						rx={(overheadWidth > 1 || hasLoad) ? 0 : 4}
						fill={gpuColor}
						opacity={isHovered ? 0.95 : 0.75}
					/>
					<!-- Right cap for rounded corners -->
					{#if overheadWidth > 1 || hasLoad}
						<rect
							x={barStartX + barWidth - 4}
							y={barY}
							width="4"
							height={BAR_HEIGHT}
							rx="4"
							fill={gpuColor}
							opacity={isHovered ? 0.95 : 0.75}
						/>
					{/if}

					<!-- Queue depth indicator -->
					{#if span.queueDepth > 0}
						<g>
							<circle
								cx={barStartX + overheadWidth + inferenceWidth - 2}
								cy={barY + 2}
								r="6"
								fill="#ef4444"
								opacity="0.9"
							/>
							<text
								x={barStartX + overheadWidth + inferenceWidth - 2}
								y={barY + 2}
								text-anchor="middle"
								dominant-baseline="central"
								fill="white"
								font-size="8"
								font-weight="700"
								font-family="ui-monospace, monospace"
							>{span.queueDepth}</text>
						</g>
					{/if}

					<!-- Duration label with inference breakdown -->
					<text
						x={barStartX + barWidth + 6}
						y={y + ROW_HEIGHT / 2}
						dominant-baseline="central"
						fill="#9ca3af"
						font-size="10"
						font-family="ui-monospace, monospace"
					>{formatDuration(spanDuration)}<tspan fill="#6b7280" font-size="9"> ({formatDuration(span.inferenceMs)} inf{#if hasLoad}, {formatDuration(loadMs)} load{/if})</tspan></text>
				{:else}
					<!-- Agent: solid bar (no inference data or stand_aside) -->
					<rect
						x={barStartX}
						y={barY}
						width={barWidth}
						height={BAR_HEIGHT}
						rx="4"
						fill={span.color}
						opacity={isHovered ? 0.95 : 0.75}
					/>

					<!-- Duration label -->
					<text
						x={barStartX + barWidth + 6}
						y={y + ROW_HEIGHT / 2}
						dominant-baseline="central"
						fill="#9ca3af"
						font-size="10"
						font-family="ui-monospace, monospace"
					>{formatDuration(spanDuration)}</text>
				{/if}

				<!-- Signal markers (advisory, triaging, evaluating, heartbeat) -->
				{#each span.signalMarkers as marker}
					{@const mx = msToX(marker.ms)}
					<line
						x1={mx}
						y1={barY + 2}
						x2={mx}
						y2={barY + BAR_HEIGHT - 2}
						stroke={marker.color}
						stroke-width="2"
						opacity={isHovered ? 1 : 0.85}
					/>
				{/each}

				<!-- Tool badges and inference info on hover -->
				{#if isHovered && (span.toolsUsed.length > 0 || span.inferenceMs > 0)}
					<foreignObject
						x={barStartX}
						y={barY + BAR_HEIGHT + 2}
						width={chartWidth}
						height="20"
					>
						<div class="tool-badges-row" xmlns="http://www.w3.org/1999/xhtml">
							{#each span.toolsUsed as tool}
								<span class="span-tool-badge">{tool}</span>
							{/each}
							{#if span.queueDepth > 0}
								<span class="span-queue-badge">queued x{span.queueDepth} on {gpuName(span.gpuLabel)}</span>
							{/if}
						</div>
					</foreignObject>
				{/if}
			</g>
		{/each}
	</svg>

	<!-- Legend -->
	<div class="span-legend">
		{#each legendItems as item}
			<div class="legend-item" title={item.tooltip}>
				{#if item.type === 'bar-solid'}
					<span class="legend-swatch" style="background-color: {item.color}; opacity: 0.75"></span>
				{:else if item.type === 'bar-faded'}
					<span class="legend-swatch" style="background-color: {item.color}; opacity: 0.25"></span>
				{:else if item.type === 'queue'}
					<span class="legend-swatch-queue">{'\u00B7'}</span>
				{:else}
					<span class="legend-swatch" style="background-color: {item.color}"></span>
				{/if}
				<span class="legend-label">{item.label}</span>
			</div>
		{/each}
	</div>
</div>

<style>
	.span-graph {
		width: 100%;
		overflow-x: hidden;
		position: relative;
	}

	.graph-controls {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.25rem 0.25rem 0.5rem;
	}

	.hidden-count {
		font-size: 0.7rem;
		color: var(--color-text-muted, #6b7280);
		opacity: 0.75;
	}

	.copy-image-btn {
		position: absolute;
		top: 4px;
		right: 4px;
		z-index: 10;
		background: rgba(30, 41, 59, 0.8);
		border: 1px solid rgba(100, 116, 139, 0.3);
		border-radius: 4px;
		padding: 4px 8px;
		cursor: pointer;
		font-size: 14px;
		line-height: 1;
		color: #94a3b8;
		transition: all 0.15s;
	}

	.copy-image-btn:hover {
		background: rgba(51, 65, 85, 0.9);
		border-color: rgba(100, 116, 139, 0.6);
		color: #e2e8f0;
	}

	.copy-image-btn.copied {
		color: #34d399;
		border-color: rgba(52, 211, 153, 0.4);
	}

	.span-graph svg {
		display: block;
	}

	.span-legend {
		display: flex;
		flex-wrap: wrap;
		gap: 0.75rem;
		padding: 0.5rem 0.75rem;
		border-top: 1px solid var(--color-border);
	}

	.legend-item {
		display: flex;
		align-items: center;
		gap: 0.375rem;
	}

	.legend-swatch {
		width: 10px;
		height: 10px;
		border-radius: 2px;
		flex-shrink: 0;
		opacity: 0.75;
	}

	.legend-swatch-queue {
		width: 14px;
		height: 14px;
		border-radius: 50%;
		flex-shrink: 0;
		background: #ef4444;
		color: white;
		font-size: 10px;
		font-weight: 700;
		display: flex;
		align-items: center;
		justify-content: center;
		line-height: 1;
	}

	.legend-label {
		font-size: 0.65rem;
		color: var(--color-text-muted);
	}

	:global(.tool-badges-row) {
		display: flex;
		gap: 4px;
	}

	:global(.span-tool-badge) {
		font-size: 9px;
		padding: 1px 5px;
		border-radius: 3px;
		background: rgba(139, 92, 246, 0.25);
		color: #a78bfa;
		font-family: ui-monospace, monospace;
		white-space: nowrap;
	}

	:global(.span-queue-badge) {
		font-size: 9px;
		padding: 1px 5px;
		border-radius: 3px;
		background: rgba(239, 68, 68, 0.25);
		color: #f87171;
		font-family: ui-monospace, monospace;
		white-space: nowrap;
	}
</style>
