<script lang="ts">
	import { resolve } from '$app/paths';
	import HealthBadge from '#lib/components/common/HealthBadge.svelte';
	import { linkHref } from '#lib/overview-link.js';
	import type { Finding } from '#lib/overview-model.js';

	interface Props {
		findings: Finding[];
	}

	let { findings }: Props = $props();
</script>

{#if findings.length > 0}
	<section class="attention" aria-labelledby="attention-title" data-testid="needs-attention">
		<h2 id="attention-title">Needs attention <span class="count">{findings.length}</span></h2>
		<ul>
			{#each findings as f (f.subject + f.problem)}
				<li>
					<a href={linkHref(f.link, resolve('/'))}>
						<HealthBadge health={f.health} />
						<span class="subject">{f.subject}</span>
						<span class="problem">{f.problem}</span>
					</a>
				</li>
			{/each}
		</ul>
	</section>
{/if}

<style>
	.attention {
		border: 1px solid var(--color-border);
		border-left: 3px solid var(--color-error);
		border-radius: 0.5rem;
		background-color: var(--color-bg-secondary);
		padding: 0.75rem 1rem;
		margin-bottom: 1.5rem;
	}
	h2 {
		font-size: 0.9rem;
		font-weight: 600;
		margin: 0 0 0.5rem;
		color: var(--color-text);
	}
	.count {
		font-weight: 500;
		color: var(--color-text-muted);
		margin-left: 0.25rem;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	li + li {
		border-top: 1px solid var(--color-border);
	}
	a {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.5rem 0.25rem;
		color: inherit;
		text-decoration: none;
		font-size: 0.85rem;
	}
	a:hover .subject {
		text-decoration: underline;
	}
	.subject {
		font-weight: 600;
		color: var(--color-text);
	}
	.problem {
		color: var(--color-text-muted);
		min-width: 0;
		overflow-wrap: anywhere;
	}
</style>
