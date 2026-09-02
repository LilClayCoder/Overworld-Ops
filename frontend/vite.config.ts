import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()],
	server: {
		proxy: {
			// Proxy the API in dev so the browser sees one origin. That keeps
			// the session cookie same-site and means no CORS configuration is
			// needed to develop against a locally running backend.
			'/api': {
				target: process.env.API_URL ?? 'http://localhost:8080',
				changeOrigin: true
			}
		}
	}
});
