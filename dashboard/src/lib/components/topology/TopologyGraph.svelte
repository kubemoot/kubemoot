<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { base } from '$app/paths';
	import { goto } from '$app/navigation';
	import type { TopologyNode, TopologyEdge } from '$types/kubemoot.js';
	import type cytoscape from 'cytoscape';
	import { CHAT_ALL, agentNodeId, parseChatSubject } from '$lib/crewScope';

	let { nodes = [], edges = [] }: { nodes: TopologyNode[]; edges: TopologyEdge[] } = $props();

	let container: HTMLDivElement;
	let cy: cytoscape.Core | undefined;
	let eventSource: EventSource | undefined;
	let layoutPositionsKey = 'kubemoot-topology-positions';

	const roleColors: Record<string, string> = {
		coordinator: '#3b82f6',
		specialist: '#10b981',
		peer: '#8b5cf6'
	};

	const statusColors: Record<string, string> = {
		ready: '#10b981',
		error: '#ef4444',
		pending: '#f59e0b'
	};

	function buildElements() {
		// Load saved positions from sessionStorage
		const savedPositions = getSavedPositions();

		// Node ids carry the namespace so same-named agents in two namespaces are
		// distinct nodes; `name` keeps the bare agent name.
		const cyNodes = nodes.map((n) => {
			const id = agentNodeId(n.namespace, n.id);
			const nodeData: any = {
				data: {
					id,
					name: n.id,
					label: n.id.replace('homelab-', ''),
					role: n.role,
					status: n.status,
					toolCount: n.toolCount,
					peerCount: n.peerCount,
					description: n.description,
					namespace: n.namespace,
					bgColor: n.status === 'ready' ? roleColors[n.role] || '#6b7280' : statusColors[n.status] || '#6b7280',
					borderColor: statusColors[n.status] || '#6b7280',
					nodeSize: n.role === 'coordinator' ? 70 : 55
				}
			};

			// Apply saved position if available
			if (savedPositions[id]) {
				nodeData.position = savedPositions[id];
			}

			return nodeData;
		});

		const cyEdges = edges.map((e) => ({
			data: {
				id: e.id,
				source: agentNodeId(e.namespace, e.source),
				target: agentNodeId(e.namespace, e.target)
			}
		}));

		return [...cyNodes, ...cyEdges];
	}

	function getSavedPositions(): Record<string, { x: number; y: number }> {
		try {
			const saved = sessionStorage.getItem(layoutPositionsKey);
			return saved ? JSON.parse(saved) : {};
		} catch {
			return {};
		}
	}

	function savePositions() {
		if (!cy) return;
		const positions: Record<string, { x: number; y: number }> = {};
		cy.nodes().forEach((node) => {
			const pos = node.position();
			positions[node.id()] = { x: pos.x, y: pos.y };
		});
		sessionStorage.setItem(layoutPositionsKey, JSON.stringify(positions));
	}

	async function initCytoscape() {
		const cytoscapeModule = await import('cytoscape');
		const dagreModule = await import('cytoscape-dagre');
		const cytoscapeFn = cytoscapeModule.default;
		const dagreFn = dagreModule.default;

		cytoscapeFn.use(dagreFn);

		const elements = buildElements();
		const hasSavedPositions = elements.some((el: any) => el.position);

		cy = cytoscapeFn({
			container,
			elements,
			style: [
				{
					selector: 'node',
					style: {
						'background-color': 'data(bgColor)',
						'border-color': 'data(borderColor)',
						'border-width': 3,
						label: 'data(label)',
						'text-valign': 'bottom',
						'text-halign': 'center',
						'font-size': '12px',
						'font-family': 'ui-monospace, monospace',
						color: '#e5e7eb',
						'text-margin-y': 10,
						width: 'data(nodeSize)',
						height: 'data(nodeSize)',
						'text-outline-color': '#111827',
						'text-outline-width': 2
					}
				},
				{
					selector: 'node[role="coordinator"]',
					style: {
						'background-image': `${base}/dashboard/coordinator.webp`,
						'background-fit': 'cover',
						'background-clip': 'none'
					}
				},
				{
					selector: 'node[role="specialist"]',
					style: {
						'background-image': `${base}/dashboard/specialist.png`,
						'background-fit': 'cover',
						'background-clip': 'none'
					}
				},
				{
					selector: 'edge',
					style: {
						width: 2,
						'line-color': '#4b5563',
						'target-arrow-color': '#4b5563',
						'target-arrow-shape': 'triangle',
						'curve-style': 'bezier',
						'arrow-scale': 1.2
					}
				},
				{
					selector: 'node:active',
					style: {
						'overlay-opacity': 0.2
					}
				},
				{
					selector: '.pulse',
					style: {
						'border-width': 6,
						'border-color': '#fbbf24'
					}
				}
			],
			layout: hasSavedPositions
				? { name: 'preset' }
				: {
					name: 'dagre',
					rankDir: 'TB',
					nodeSep: 80,
					rankSep: 100,
					padding: 40
				} as cytoscape.LayoutOptions,
			userZoomingEnabled: true,
			userPanningEnabled: true,
			boxSelectionEnabled: false,
			minZoom: 0.3,
			maxZoom: 3
		});

		// Save positions after initial layout
		if (!hasSavedPositions) {
			setTimeout(() => savePositions(), 500);
		}

		// Click node to navigate to agent detail
		cy.on('tap', 'node', (evt) => {
			const node = evt.target;
			const name = node.data('name');
			const ns = node.data('namespace');
			goto(`${base}/agents/${name}?namespace=${ns}`);
		});

		// Tooltip on hover
		cy.on('mouseover', 'node', (evt) => {
			const node = evt.target;
			container.style.cursor = 'pointer';
			node.style('border-width', 5);
		});

		cy.on('mouseout', 'node', (evt) => {
			const node = evt.target;
			container.style.cursor = 'default';
			if (!node.hasClass('pulse')) {
				node.style('border-width', 3);
			}
		});

		// Save positions on drag end
		cy.on('dragfree', 'node', () => {
			savePositions();
		});
	}

	function subscribeToEvents() {
		eventSource = new EventSource(`${base}/api/nats/subscribe?subject=${encodeURIComponent(CHAT_ALL)}`);

		eventSource.onmessage = (event) => {
			try {
				const msg = JSON.parse(event.data);
				if (msg.type !== 'message' || !msg.data) return;

				// kubemoot.chat.<namespace>.<agent>: pulse the agent of that
				// namespace only. NATS agent tokens use underscores, node ids hyphens.
				const chat = parseChatSubject(msg.subject);
				if (!chat || !cy) return;
				const node = cy.getElementById(agentNodeId(chat.namespace, chat.agent.replaceAll('_', '-')));
				if (node.length === 0) return;

				// Pulse animation
				node.addClass('pulse');
				setTimeout(() => node.removeClass('pulse'), 1500);

				// Also pulse the edge from coordinator to this specialist
				const incomingEdges = node.incomers('edge');
				incomingEdges.forEach((edge: cytoscape.EdgeSingular) => {
					edge.style('line-color', '#fbbf24');
					edge.style('target-arrow-color', '#fbbf24');
					edge.style('width', 4);
					setTimeout(() => {
						edge.style('line-color', '#4b5563');
						edge.style('target-arrow-color', '#4b5563');
						edge.style('width', 2);
					}, 1500);
				});
			} catch {
				// Ignore parse errors
			}
		};
	}

	// Update graph when data changes
	$effect(() => {
		if (cy && nodes.length > 0) {
			cy.elements().remove();
			cy.add(buildElements());
			cy.layout({
				name: 'dagre',
				rankDir: 'TB',
				nodeSep: 80,
				rankSep: 100,
				padding: 40
			} as cytoscape.LayoutOptions).run();
		}
	});

	onMount(() => {
		initCytoscape();
		subscribeToEvents();
	});

	onDestroy(() => {
		if (eventSource) {
			eventSource.close();
		}
		if (cy) {
			cy.destroy();
		}
	});
</script>

<div class="graph-container" bind:this={container}></div>

<style>
	.graph-container {
		width: 100%;
		height: 100%;
		min-height: 500px;
		background-color: var(--color-bg);
		border-radius: 0.5rem;
		border: 1px solid var(--color-border);
	}
</style>
