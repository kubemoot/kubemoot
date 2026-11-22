<script lang="ts">
	import StatusBadge from './StatusBadge.svelte';
	import { healthBadge, healthLabel, type Health } from '#lib/health.js';

	interface Props {
		health: Health;
		size?: 'sm' | 'md';
	}

	let { health, size = 'sm' }: Props = $props();
</script>

<StatusBadge status={healthBadge(health)} label={healthLabel(health)} {size} />
