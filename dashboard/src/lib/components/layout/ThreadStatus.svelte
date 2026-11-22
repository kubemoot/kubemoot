<script lang="ts">
	import { statusLabel } from '#lib/thread-status.js';

	// A thread's state in a word, coloured the same on every list and header.
	let { status }: { status: string } = $props();
</script>

<span class="status" class:open={status === 'open'} class:synthesized={status === 'synthesized'}>{statusLabel(status)}</span>

<style>
	.status { font-size: 0.65rem; font-weight: 500; text-transform: uppercase; color: var(--color-text-muted); }
	.status.open { color: #10b981; }
	.status.synthesized { color: #f59e0b; }
</style>
