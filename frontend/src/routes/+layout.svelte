<script lang="ts">
	import '../app.css';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { session } from '$lib/session.svelte';

	let { children } = $props();

	// Resolve the session once on mount, then send anyone unauthenticated to
	// the login page. Auth is enforced by the API too; this is only so the UI
	// does not render an empty dashboard.
	$effect(() => {
		if (session.loading) {
			session.refresh();
			return;
		}
		if (!session.user && page.url.pathname !== '/login') {
			goto('/login');
		}
	});

	async function handleLogout() {
		await session.logout();
		goto('/login');
	}
</script>

<header>
	<div class="container bar">
		<a class="brand" href="/">Overworld&nbsp;Ops</a>
		{#if session.user}
			<nav>
				<a href="/">Servers</a>
				<a href="/new">New server</a>
				<span class="muted">{session.user.username}</span>
				<button onclick={handleLogout}>Log out</button>
			</nav>
		{/if}
	</div>
</header>

<main class="container">
	{#if session.loading}
		<p class="muted">Loading…</p>
	{:else}
		{@render children()}
	{/if}
</main>

<style>
	header {
		border-bottom: 1px solid var(--border);
		background: var(--surface);
	}

	.bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		padding-block: 0.9rem;
	}

	.brand {
		font-weight: 700;
		font-size: 1.05rem;
		color: var(--text);
		text-decoration: none;
	}

	nav {
		display: flex;
		align-items: center;
		gap: 1rem;
		font-size: 0.9rem;
	}
</style>
