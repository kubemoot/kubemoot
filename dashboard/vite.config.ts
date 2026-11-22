import adapter from '@sveltejs/adapter-node';
import { sveltekit } from '@sveltejs/kit/vite';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		sveltekit({
			preprocess: vitePreprocess(),
			adapter: adapter({
				out: 'build',
				precompress: false,
				envPrefix: ''
			}),
			paths: {
				base: '/dashboard',
				// resolve() answers an absolute path everywhere: the sign-in gate builds its redirects and
				// cookie path from it, and a path relative to the request would loop on /signin
				relative: false
			},
			// SvelteKit's own check compares a form post's Origin with an https:// guess when no
			// ORIGIN is set, which refuses a kubectl port-forward. src/hooks.server.ts checks the
			// Origin of every write, of any content type, against the origin the client used; that
			// is the one check.
			csrf: { trustedOrigins: ['*'] }
		})
	],
	server: {
		port: 5173,
		host: true
	}
});
