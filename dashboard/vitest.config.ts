import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { fileURLToPath } from 'node:url';

const r = (p: string) => fileURLToPath(new URL(p, import.meta.url));

// Separate from vite.config.ts (which uses the sveltekit() plugin for build/dev):
// component/store tests compile Svelte with the plain svelte() plugin under jsdom,
// with the browser resolve condition so Svelte 5 mounts client-side rather than SSR.
// SvelteKit's virtual modules ($app/*) and path aliases ($lib, $types) are
// stubbed/mapped here so store modules that import them are testable without the
// full kit runtime.
export default defineConfig({
	plugins: [svelte()],
	resolve: {
		conditions: ['browser'],
		alias: {
			$lib: r('./src/lib'),
			$types: r('./src/lib/types'),
			$stores: r('./src/lib/stores'),
			'$app/environment': r('./vitest-mocks/app-environment.ts'),
			'$app/paths': r('./vitest-mocks/app-paths.ts'),
			'$env/dynamic/private': r('./vitest-mocks/env-dynamic-private.ts')
		}
	},
	test: {
		environment: 'jsdom',
		globals: true,
		include: ['src/**/*.{test,spec}.{ts,js}'],
		coverage: {
			provider: 'v8',
			include: ['src/**/*.{ts,svelte}'],
			exclude: ['src/**/*.{test,spec}.ts', 'src/**/*.d.ts'],
			// SonarQube scans from the repository root, so the lcov file paths are written
			// relative to it (dashboard/src/...), one level above this config.
			reporter: ['text-summary', ['lcov', { projectRoot: '..' }]]
		}
	}
});
