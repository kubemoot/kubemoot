<script lang="ts">
	import { errorOnFailure, loadingOnStart } from '#lib/client/refresh-state.js';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { refreshTrigger } from '#lib/stores/index.js';
	import { DetailPanel } from '#lib/components/layout/index.js';
	import { Section, InfoRow } from '#lib/components/common/index.js';
	import type { CrewFitnessSuite } from '#lib/types/kubemoot.js';

	let suite = $state<CrewFitnessSuite | null>(null);
	let loaded = false;
	let loading = $state(true);
	let error = $state<string | null>(null);

	const name = $derived(page.params.name as string);
	const ns = $derived(page.url.searchParams.get('namespace') || 'kubemoot');

	async function fetchSuite() {
		loading = loadingOnStart(loaded);
		error = null;
		try {
			const res = await fetch(resolve('/api/kubemoot/crewfitnesssuites/[namespace]/[name]', { namespace: ns, name }));
			const data = await res.json();
			if (!res.ok || data.error) throw new Error(data.error ?? 'Fitness suite not found');
			suite = data;
		} catch (e) {
			error = errorOnFailure(loaded, e, 'Failed to fetch fitness suite');
		} finally {
			loaded = loaded || error === null;
			loading = false;
		}
	}

	$effect(() => {
		ns;
		name;
		$refreshTrigger;
		fetchSuite();
	});
</script>

<DetailPanel route="/crewfitnesssuites/[name]" title={name} subtitle="Fitness suite" {loading} {error} resource={suite}>
	{#snippet children()}
		{#if suite}
			<div class="sections">
				<Section title="Spec">
					<InfoRow label="Crew" value={suite.spec.crewRef} />
					<InfoRow label="Description" value={suite.spec.description} />
					<InfoRow label="Trials" value={suite.spec.iterations} />
					<InfoRow label="Concurrency" value={suite.spec.concurrency} />
					<InfoRow label="Purpose" value={suite.spec.purpose} />
					<InfoRow label="Trial isolation" value={suite.spec.trialIsolation} />
					<InfoRow label="Scenarios" value={suite.spec.scripts?.length ?? 0} />
				</Section>

				<Section title="Status">
					<InfoRow label="Phase" value={suite.status?.phase} />
					<InfoRow label="Run" value={suite.status?.runId} mono />
					<InfoRow label="Started" value={suite.status?.startedAt} />
					<InfoRow label="Completed" value={suite.status?.completedAt} />
					<InfoRow label="Trials finished" value="{suite.status?.iterationsCompleted ?? 0} / {suite.status?.iterationsTotal ?? 0}" />
					<InfoRow label="Passed" value={suite.status?.passed ?? 0} />
					<InfoRow label="Failed" value={suite.status?.failed ?? 0} />
					<InfoRow label="Errored" value={suite.status?.errored ?? 0} />
					<InfoRow label="Error" value={suite.status?.error} />
				</Section>

				<p class="back"><a href="{resolve('/crewfitnesssuites')}">&larr; All fitness suites</a></p>
			</div>
		{/if}
	{/snippet}
</DetailPanel>

<style>
	.sections {
		display: flex;
		flex-direction: column;
		gap: 1.5rem;
	}

	.back {
		font-size: 0.85rem;
	}
</style>
