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
				base: '/dashboard'
			}
		})
	],
	server: {
		port: 5173,
		host: true
	}
});
