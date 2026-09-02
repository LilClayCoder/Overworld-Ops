<script lang="ts">
	import { page } from '$app/state';
	import { api, displayStatus, isJoinable, type MinecraftServer } from '$lib/api';

	const id = $derived(page.params.id!);

	let server = $state<MinecraftServer | null>(null);
	let error = $state('');
	let busy = $state(false);

	/** Console lines. Capped so a long-running server cannot grow the tab
	 *  without bound — the full history stays in Docker's log driver. */
	const MAX_LINES = 1000;
	let lines = $state<string[]>([]);
	let consoleEl = $state<HTMLElement | null>(null);
	let follow = $state(true);

	const serverState = $derived(server ? displayStatus(server) : 'loading');

	async function load() {
		try {
			server = await api.getServer(id);
			error = '';
		} catch (err) {
			error = err instanceof Error ? err.message : 'could not load server';
		}
	}

	$effect(() => {
		load();
		const timer = setInterval(load, 5000);
		return () => clearInterval(timer);
	});

	// Tail the container log over SSE. EventSource reconnects on its own, so a
	// server restart re-attaches the console without a page reload.
	$effect(() => {
		if (!server?.containerId) return;

		const source = new EventSource(api.logStreamUrl(id));

		source.onmessage = (event) => {
			const line = JSON.parse(event.data) as { stream: string; text: string };
			lines = [...lines, line.text].slice(-MAX_LINES);
		};
		source.onerror = () => {
			// EventSource retries by itself; nothing to do but let it.
		};

		return () => source.close();
	});

	// Keep the newest line in view, unless the reader has scrolled up to read
	// something — then leave their scroll position alone.
	$effect(() => {
		void lines;
		if (follow && consoleEl) {
			consoleEl.scrollTop = consoleEl.scrollHeight;
		}
	});

	function onScroll() {
		if (!consoleEl) return;
		const distanceFromBottom =
			consoleEl.scrollHeight - consoleEl.scrollTop - consoleEl.clientHeight;
		follow = distanceFromBottom < 40;
	}

	async function act(fn: (id: string) => Promise<unknown>) {
		busy = true;
		try {
			await fn(id);
			await load();
		} catch (err) {
			error = err instanceof Error ? err.message : 'action failed';
		} finally {
			busy = false;
		}
	}
</script>

{#if error}
	<p class="error">{error}</p>
{/if}

{#if server}
	<div class="head">
		<div>
			<h1>{server.name}</h1>
			<p class="muted">
				{server.type}
				{server.version}
				{#if server.modpackId}· {server.modpackProvider}/{server.modpackId}{/if}
				· {server.memory} · {server.cpuLimit} cores
			</p>
		</div>

		<div class="actions">
			{#if serverState === 'running' || serverState === 'starting'}
				<button disabled={busy} onclick={() => act(api.stopServer)}>Stop</button>
			{:else}
				<button class="primary" disabled={busy} onclick={() => act(api.startServer)}>Start</button>
			{/if}
		</div>
	</div>

	<div class="card connect">
		<span class="badge {serverState}">{serverState}</span>
		{#if isJoinable(server)}
			<span>Connect at <code>{server.address}</code></span>
		{:else if serverState === 'starting'}
			<span class="muted">Starting — modpacks can take several minutes on first boot.</span>
		{:else}
			<span class="muted">Server is {serverState}. Address will be <code>{server.address}</code>.</span>
		{/if}
	</div>

	<h2>Console</h2>
	<div class="console mono" bind:this={consoleEl} onscroll={onScroll}>
		{#each lines as line, i (i)}
			<div class="line">{line}</div>
		{:else}
			<div class="muted">Waiting for output…</div>
		{/each}
	</div>
	{#if !follow}
		<button class="jump" onclick={() => (follow = true)}>Jump to latest</button>
	{/if}
{:else if !error}
	<p class="muted">Loading…</p>
{/if}

<style>
	.head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 1rem;
		flex-wrap: wrap;
	}

	h1 {
		margin-bottom: 0.2rem;
	}

	.head p {
		margin-top: 0;
	}

	.connect {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex-wrap: wrap;
		margin-bottom: 1.5rem;
	}

	.connect code {
		color: var(--accent);
	}

	.console {
		height: 26rem;
		overflow-y: auto;
		background: #0e1013;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 0.75rem 1rem;
		font-size: 0.82rem;
		line-height: 1.45;
	}

	.line {
		white-space: pre-wrap;
		word-break: break-word;
	}

	.jump {
		margin-top: 0.5rem;
	}

	.badge {
		font-size: 0.72rem;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		padding: 0.1rem 0.45rem;
		border-radius: 999px;
		border: 1px solid var(--border);
		color: var(--text-dim);
		font-weight: 600;
	}

	.badge.running {
		color: var(--accent);
		border-color: var(--accent-dim);
	}

	.badge.starting {
		color: var(--warn);
		border-color: var(--warn);
	}

	.badge.error {
		color: var(--danger);
		border-color: var(--danger);
	}
</style>
