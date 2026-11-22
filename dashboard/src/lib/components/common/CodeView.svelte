<script lang="ts">
	import type { YamlToken } from '#lib/yaml-highlight.js';
	import type { TextFormat } from '#lib/resource-view.js';

	interface Props {
		/** The text as coloured pieces, line by line. */
		lines: YamlToken[][];
		format: TextFormat;
	}

	let { lines, format }: Props = $props();
</script>

<div class="code-view">
	<!-- A scrollable region must be reachable by keyboard so long lines can be read. -->
	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<pre class="code {format}" role="region" aria-label="Resource {format.toUpperCase()}" tabindex="0"><code
			>{#each lines as tokens, i (i)}<span class="line">{#each tokens as token, j (j)}<span class={token.kind}
							>{token.text}</span
						>{/each}{'\n'}</span
				>{/each}</code
		></pre>
</div>

<style>
	.code-view {
		--yaml-key: #93c5fd;
		--yaml-string: #86efac;
		--yaml-number: #fcd34d;
		--yaml-bool: #d8b4fe;
		--yaml-null: #94a3b8;
		--yaml-punct: #94a3b8;
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		background-color: var(--color-bg);
		overflow: hidden;
	}

	.code:focus-visible {
		outline: 2px solid var(--color-primary);
		outline-offset: -2px;
	}

	.code {
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

</style>
