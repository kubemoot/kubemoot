<script lang="ts">
	import { namespace } from '#lib/stores/index.js';
	import { entryLabel, technicalNameHint, type CrewEntry } from '#lib/crew-display-name.js';

	interface Props {
		crews: CrewEntry[];
		loading?: boolean;
	}

	let { crews, loading = false }: Props = $props();

	// The store value is a NAMESPACE (pages filter by namespace). '' = All crews.
	function handleChange(event: Event) {
		const select = event.target as HTMLSelectElement;
		namespace.set(select.value);
	}

	// Guard against a stale stored selection: if a previously-chosen value (e.g.
	// from the old all-namespaces selector, or a crew that's since been removed)
	// isn't among the current crew namespaces, fall back to "All crews".
	$effect(() => {
		if (loading) return;
		const current = $namespace;
		if (current !== '' && !crews.some((c) => c.namespace === current)) {
			namespace.set('');
		}
	});
</script>

<div class="namespace-selector">
	<label for="crew-select">Crew:</label>
	{#if loading}
		<div class="loading">Loading...</div>
	{:else}
		<select id="crew-select" value={$namespace} onchange={handleChange}>
			<option value="" class="all-crews">All crews</option>
			{#if crews.length}
				<option disabled>───────────</option>
				{#each crews as c (c.namespace)}
					{@const label = entryLabel(c)}
					{@const hint = technicalNameHint(label, c.crew)}
					<option value={c.namespace} title={hint}>{label}{hint ? ` (${hint})` : ''}</option>
				{/each}
			{/if}
		</select>
	{/if}
</div>

<style>
	.namespace-selector {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	label {
		font-size: 0.8rem;
		color: var(--color-text-muted);
		white-space: nowrap;
	}

	select {
		background-color: var(--color-bg-secondary);
		color: var(--color-text);
		border: 1px solid var(--color-border);
		border-radius: 0.375rem;
		padding: 0.375rem 2rem 0.375rem 0.75rem;
		font-size: 0.8rem;
		cursor: pointer;
		appearance: none;
		background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 24 24' fill='none' stroke='%2394a3b8' stroke-width='2'%3E%3Cpolyline points='6 9 12 15 18 9'%3E%3C/polyline%3E%3C/svg%3E");
		background-repeat: no-repeat;
		background-position: right 0.5rem center;
		min-width: 150px;
	}

	select:hover {
		border-color: var(--color-primary);
	}

	select:focus {
		outline: none;
		border-color: var(--color-primary);
		box-shadow: 0 0 0 2px rgba(59, 130, 246, 0.2);
	}

	.loading {
		font-size: 0.8rem;
		color: var(--color-text-muted);
	}

	option.all-crews {
		font-style: italic;
	}
</style>
