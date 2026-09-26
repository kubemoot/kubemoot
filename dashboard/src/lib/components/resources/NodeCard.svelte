<script lang="ts">
	import type { NodeWithGPU } from '$types/k8s.js';
	import StatusBadge from '../common/StatusBadge.svelte';

	interface Props {
		node: NodeWithGPU;
	}

	let { node }: Props = $props();

	function formatStatus(s: string): string {
		return s.replace(/([a-z])([A-Z])/g, '$1 $2');
	}

	const isReady = $derived(
		node.status?.conditions?.find(c => c.type === 'Ready')?.status === 'True'
	);

	const status = $derived(isReady ? 'success' : 'error');
	const statusLabel = $derived(formatStatus(isReady ? 'Ready' : 'NotReady'));

	const internalIP = $derived(
		node.status?.addresses?.find(a => a.type === 'InternalIP')?.address
	);

	const roles = $derived(() => {
		const labels = node.metadata?.labels || {};
		const roleLabels = Object.keys(labels).filter(k => k.startsWith('node-role.kubernetes.io/'));
		return roleLabels.map(k => k.replace('node-role.kubernetes.io/', ''));
	});
</script>

<div class="card">
	<header class="header">
		<div class="title-section">
			<span class="kind">Node</span>
			<h3 class="name">{node.metadata.name}</h3>
		</div>
		<StatusBadge {status} label={statusLabel} size="sm" />
	</header>

	<div class="body">
		<div class="info">
			{#if internalIP}
				<div class="row">
					<span class="label">IP</span>
					<span class="value mono">{internalIP}</span>
				</div>
			{/if}
			{#if node.status?.nodeInfo?.kubeletVersion}
				<div class="row">
					<span class="label">Kubelet</span>
					<span class="value">{node.status.nodeInfo.kubeletVersion}</span>
				</div>
			{/if}
			{#if node.status?.nodeInfo?.osImage}
				<div class="row">
					<span class="label">OS</span>
					<span class="value os">{node.status.nodeInfo.osImage}</span>
				</div>
			{/if}
		</div>
	</div>

	{#if node.gpu?.present}
		<footer class="footer gpu">
			<div class="gpu-info">
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<rect x="2" y="6" width="20" height="12" rx="2" />
					<path d="M6 12h4" />
					<path d="M14 12h4" />
				</svg>
				<span class="gpu-type">{node.gpu.type || 'GPU'}</span>
				{#if node.gpu.count && node.gpu.count > 1}
					<span class="gpu-count">x{node.gpu.count}</span>
				{/if}
				{#if node.gpu.memory}
					<span class="gpu-memory">{node.gpu.memory}</span>
				{/if}
			</div>
		</footer>
	{/if}
</div>

<style>
	.card {
		display: flex;
		flex-direction: column;
		background-color: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 1rem;
	}

	.header {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
	}

	.title-section {
		min-width: 0;
		flex: 1;
	}

	.kind {
		font-size: 0.7rem;
		font-weight: 500;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.name {
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text);
		margin-top: 0.125rem;
	}

	.body {
		margin-top: 0.875rem;
		padding-top: 0.875rem;
		border-top: 1px solid var(--color-border);
	}

	.info {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 0.8rem;
	}

	.label {
		color: var(--color-text-muted);
	}

	.value {
		color: var(--color-text);
	}

	.value.mono {
		font-family: var(--font-mono);
		font-size: 0.75rem;
	}

	.value.os {
		font-size: 0.7rem;
		max-width: 180px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.footer {
		margin-top: 0.75rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--color-border);
	}

	.footer.gpu {
		background-color: rgba(168, 85, 247, 0.1);
		margin: 0.75rem -1rem -1rem;
		padding: 0.75rem 1rem;
		border-radius: 0 0 0.5rem 0.5rem;
		border-top: 1px solid rgba(168, 85, 247, 0.2);
	}

	.gpu-info {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		color: var(--color-purple);
		font-size: 0.8rem;
		font-weight: 500;
	}

	.gpu-type {
		flex: 1;
	}

	.gpu-count {
		font-family: var(--font-mono);
		font-size: 0.75rem;
	}

	.gpu-memory {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		opacity: 0.8;
	}
</style>
