import adapter from '@sveltejs/adapter-node';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	preprocess: vitePreprocess(),
	kit: {
		// adapter-node produces a plain Node server, which is the easiest
		// thing to drop into a container next to the Go API.
		adapter: adapter()
	}
};

export default config;
