<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title: string;
		children: Snippet;
		collapsible?: boolean;
		defaultOpen?: boolean;
	}

	let { title, children, collapsible = false, defaultOpen = true }: Props = $props();

	let isOpen = $state(defaultOpen);

	function toggle() {
		if (collapsible) {
			isOpen = !isOpen;
		}
	}
</script>

<section class="section">
	<header
		class="header"
		class:collapsible
		onclick={toggle}
		role={collapsible ? 'button' : undefined}
		tabindex={collapsible ? 0 : undefined}
		onkeydown={(e) => e.key === 'Enter' && toggle()}
	>
		<h3 class="title">{title}</h3>
		{#if collapsible}
			<svg
				width="16"
				height="16"
				viewBox="0 0 24 24"
				fill="none"
				stroke="currentColor"
				stroke-width="2"
				class="chevron"
				class:open={isOpen}
			>
				<polyline points="6 9 12 15 18 9" />
			</svg>
		{/if}
	</header>
	{#if isOpen}
		<div class="content">
			{@render children()}
		</div>
	{/if}
</section>

<style>
	.section {
		background-color: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		overflow: hidden;
	}

	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 0.875rem 1rem;
		background-color: var(--color-bg-tertiary);
		border-bottom: 1px solid var(--color-border);
	}

	.header.collapsible {
		cursor: pointer;
		user-select: none;
	}

	.header.collapsible:hover {
		background-color: rgba(51, 65, 85, 0.7);
	}

	.title {
		font-size: 0.9rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.chevron {
		color: var(--color-text-muted);
		transition: transform 0.2s;
	}

	.chevron.open {
		transform: rotate(180deg);
	}

	.content {
		padding: 1rem;
	}
</style>
