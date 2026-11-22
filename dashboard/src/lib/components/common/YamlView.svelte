<script lang="ts">
	import { displayResource, hasManagedFields, toYaml, type YamlResource } from '#lib/yaml.js';
	import { highlightYaml } from '#lib/yaml-highlight.js';

	interface Props {
		/** The custom resource to show; nothing renders until it has loaded. */
		resource: YamlResource | null | undefined;
	}

	let { resource }: Props = $props();

	let showManaged = $state(false);
	let copied = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;

	const text = $derived(resource ? toYaml(displayResource(resource, showManaged)) : '');
	const lines = $derived(highlightYaml(text));
	const hasManaged = $derived(resource ? hasManagedFields(resource) : false);

	async function copy() {
		try {
			await navigator.clipboard.writeText(text);
			copied = true;
			clearTimeout(timer);
			timer = setTimeout(() => (copied = false), 1500);
		} catch {
			copied = false;
		}
	}
</script>

{#if resource}
	<div class="yaml-view">
		<div class="toolbar">
			{#if hasManaged}
				<button type="button" class="tool" aria-pressed={showManaged} onclick={() => (showManaged = !showManaged)}>
					{showManaged ? 'Hide managed fields' : 'Show managed fields'}
				</button>
			{/if}
			<button type="button" class="tool copy" onclick={copy} aria-label="Copy the resource YAML to the clipboard">
				{copied ? 'Copied' : 'Copy'}
			</button>
			<span class="sr-only" role="status">{copied ? 'YAML copied to the clipboard' : ''}</span>
		</div>
		<!-- A scrollable region must be reachable by keyboard so long lines can be read. -->
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<pre class="yaml" role="region" aria-label="Resource YAML" tabindex="0"><code
				>{#each lines as tokens, i (i)}<span class="line">{#each tokens as token, j (j)}<span class={token.kind}
								>{token.text}</span
							>{/each}{'\n'}</span
					>{/each}</code
			></pre>
	</div>
{/if}

<style>
	.yaml-view {
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		background-color: var(--color-bg);
		overflow: hidden;
	}

	.toolbar {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		padding: 0.5rem 0.75rem;
		border-bottom: 1px solid var(--color-border);
		background-color: var(--color-bg-secondary);
	}

	.tool {
		min-height: 1.75rem;
		padding: 0.2rem 0.75rem;
		font-size: 0.75rem;
		border-radius: 0.25rem;
		border: 1px solid var(--color-border);
		background-color: var(--color-bg-tertiary);
		color: var(--color-text);
		cursor: pointer;
	}

	.tool:hover,
	.tool[aria-pressed='true'] {
		border-color: var(--color-primary);
	}

	.tool:focus-visible,
	.yaml:focus-visible {
		outline: 2px solid var(--color-primary);
		outline-offset: -2px;
	}

	.copy {
		min-width: 4.5rem;
	}

	.yaml {
		margin: 0;
		padding: 0.75rem 1rem 0.75rem 0;
		max-height: calc(100vh - 18rem);
		min-height: 12rem;
		overflow: auto;
		white-space: pre;
		counter-reset: line;
		font-family: var(--font-mono);
		font-size: 0.8rem;
		line-height: 1.55;
		color: var(--color-text);
		tab-size: 2;
	}

	.line::before {
		counter-increment: line;
		content: counter(line);
		display: inline-block;
		width: 3.5rem;
		padding-right: 1rem;
		text-align: right;
		color: var(--color-text-muted);
		opacity: 0.7;
		user-select: none;
		position: sticky;
		left: 0;
		background-color: var(--color-bg);
	}

	.key {
		color: var(--yaml-key);
	}
	.string {
		color: var(--yaml-string);
	}
	.number {
		color: var(--yaml-number);
	}
	.bool {
		color: var(--yaml-bool);
	}
	.null {
		color: var(--yaml-null);
		font-style: italic;
	}
	.punct {
		color: var(--yaml-punct);
	}

	.sr-only {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip-path: inset(50%);
		white-space: nowrap;
	}
</style>
