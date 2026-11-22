<script lang="ts">
	// The crew's name as a pill, the same on every list and header.
	let { label, title }: { label: string; title?: string } = $props();
</script>

<span class="crew-badge" {title}>{label}</span>

<style>
	.crew-badge {
		display: inline-block; padding: 0.125rem 0.5rem; border-radius: 1rem; font-size: 0.65rem; font-weight: 600;
		background: rgba(168, 85, 247, 0.15); color: var(--color-purple); font-family: var(--font-mono);
		overflow-wrap: anywhere;
	}
</style>
