<script lang="ts">
	import { asset } from '$app/paths';
	import { KUBEMOOT_SITE } from '#lib/brand-links.js';

	// The horizontal lockup with the name in white, the brand form for dark backgrounds.
	// The file is a copy of the brand repository's lockup, pinned in brand.lock.
	const src = asset('kubemoot-horizontal-white-text.svg');
</script>

<a href={KUBEMOOT_SITE} target="_blank" rel="noopener noreferrer" class="logo-link">
	<img class="kubemoot-logo" {src} alt="Kubemoot" width="128" height="36" />
</a>

<style>
	.logo-link {
		display: block;
	}

	.kubemoot-logo {
		display: block;
		height: 36px;
		width: auto;
	}
</style>
