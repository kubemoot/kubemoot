<script lang="ts">
	import { displayResource, toYaml } from '#lib/yaml.js';

	interface Props {
		/** The custom resource to show; nothing renders until it has loaded. */
		resource: { metadata?: Record<string, unknown> } | null | undefined;
	}

	let { resource }: Props = $props();

	const text = $derived(resource ? toYaml(displayResource(resource)) : '');
	let copied = $state(false);

	async function copy() {
		try {
			await navigator.clipboard.writeText(text);
			copied = true;
			setTimeout(() => (copied = false), 1500);
		} catch {
			copied = false;
		}
	}
</script>

{#if resource}
	<details class="yaml-view">
		<summary>YAML</summary>
		<div class="body">
			<button type="button" class="copy" onclick={copy} aria-label="Copy the resource YAML to the clipboard">
				{copied ? 'Copied' : 'Copy'}
			</button>
			<pre class="yaml" aria-label="Resource YAML"><code>{text}</code></pre>
		</div>
	</details>
{/if}

<style>
	.yaml-view {
		margin-top: 1.5rem;
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		background-color: var(--color-bg-secondary);
	}

	summary {
		cursor: pointer;
		padding: 0.6rem 1rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.body {
		position: relative;
		border-top: 1px solid var(--color-border);
	}

	.copy {
		position: absolute;
		top: 0.5rem;
		right: 0.75rem;
		padding: 0.2rem 0.7rem;
		font-size: 0.75rem;
		border-radius: 0.25rem;
		border: 1px solid var(--color-border);
		background-color: var(--color-bg-tertiary);
		color: var(--color-text);
		cursor: pointer;
	}

	.copy:hover {
		background-color: var(--color-bg-secondary);
	}

	.yaml {
		margin: 0;
		padding: 1rem;
		max-height: 32rem;
		overflow: auto;
		font-family: var(--font-mono);
		font-size: 0.75rem;
		line-height: 1.5;
		color: var(--color-text);
	}
</style>
