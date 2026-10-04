<script lang="ts">
	import { asset } from '$app/paths';

	// The horizontal lockup with the name in white, the brand form for dark backgrounds.
	// The file is a copy of the brand repository's lockup, pinned in brand.lock.
	const src = asset('kubemoot-horizontal-white-text.svg');
</script>

<img class="kubemoot-logo" {src} alt="Kubemoot" width="128" height="36" />

<style>
	.kubemoot-logo {
		display: block;
		height: 36px;
		width: auto;
	}
</style>
