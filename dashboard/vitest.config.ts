import { defineConfig } from 'vitest/config';
import { svelte, vitePreprocess } from '@sveltejs/vite-plugin-svelte';
import { fileURLToPath } from 'node:url';

const r = (p: string) => fileURLToPath(new URL(p, import.meta.url));

// Separate from vite.config.ts (which uses the sveltekit() plugin for build/dev):
// component/store tests compile Svelte with the plain svelte() plugin under jsdom,
// with the browser resolve condition so Svelte 5 mounts client-side rather than SSR.
// SvelteKit's virtual modules ($app/*) are stubbed here so store modules that import
// them are testable without the full kit runtime; #lib resolves through the
// package.json imports field.
export default defineConfig({
	// No config file: the preprocessor is the one the sveltekit() plugin uses in vite.config.ts.
	plugins: [svelte({ configFile: false, preprocess: vitePreprocess() })],
	resolve: {
		conditions: ['browser'],
		alias: {
			'$app/env/private': r('./vitest-mocks/app-env-private.ts'),
			'$app/env': r('./vitest-mocks/app-env.ts'),
			'$app/paths': r('./vitest-mocks/app-paths.ts')
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
