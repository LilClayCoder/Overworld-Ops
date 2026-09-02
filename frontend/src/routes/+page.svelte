<script lang="ts">
	import { api, displayStatus, isJoinable, type MinecraftServer } from '$lib/api';
	import { session } from '$lib/session.svelte';

	let servers = $state<MinecraftServer[]>([]);
	let error = $state('');
	let loading = $state(true);
	/** IDs with a start/stop request in flight, so buttons can be disabled. */
	let busy = $state(new Set<string>());

	async function load() {
		try {
			servers = await api.listServers();
			error = '';
		} catch (err) {
			error = err instanceof Error ? err.message : 'could not load servers';
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		if (!session.user) return;
		load();

		// Poll while the page is open. A modpack can take minutes to go from
		// "starting" to healthy, and polling is enough for a dashboard this
		// small — no need for a second websocket alongside the console.
		const timer = setInterval(load, 5000);
		return () => clearInterval(timer);
	});

	async function act(id: string, fn: (id: string) => Promise<unknown>) {
		busy = new Set(busy).add(id);
		try {
			await fn(id);
			await load();
		} catch (err) {
			error = err instanceof Error ? err.message : 'action failed';
		} finally {
			const next = new Set(busy);
			next.delete(id);
			busy = next;
		}
	}

	async function remove(server: MinecraftServer) {
		if (!confirm(`Delete "${server.name}"? Its container will be removed.`)) return;

		// Asked separately, and defaulting to "keep", because losing a world by
		// misreading one dialog is not a recoverable mistake.
		const deleteData = confirm(
			'Also delete the world data permanently?\n\n' +
				'OK — delete the world too. This cannot be undone.\n' +
				'Cancel — keep the world volume so the server can be recreated.'
		);
		await act(server.id, (id) => api.deleteServer(id, deleteData));
	}
</script>

<h1>Your servers</h1>

{#if error}
	<p class="error">{error}</p>
{/if}

{#if loading}
	<p class="muted">Loading servers…</p>
{:else if servers.length === 0}
	<div class="card empty">
		<p>No servers yet.</p>
		<a href="/new"><button class="primary">Create your first server</button></a>
	</div>
{:else}
	<ul class="list">
		{#each servers as server (server.id)}
			{@const serverState = displayStatus(server)}
			<li class="card row">
				<div class="info">
					<div class="title">
						<a href="/servers/{server.id}">{server.name}</a>
						<span class="badge {serverState}">{serverState}</span>
					</div>
					<div class="muted">
						{server.type}
						{server.version}
						{#if server.modpackId}· {server.modpackProvider}/{server.modpackId}{/if}
						· {server.memory}
					</div>
					{#if isJoinable(server)}
						<div class="mono address">{server.address}</div>
					{:else if serverState === 'starting'}
						<div class="muted">Starting up — this can take a few minutes for modpacks.</div>
					{:else if server.statusMessage}
						<div class="muted err">{server.statusMessage}</div>
					{/if}
				</div>

				<div class="actions">
					{#if serverState === 'running' || serverState === 'starting'}
						<button disabled={busy.has(server.id)} onclick={() => act(server.id, api.stopServer)}>
							Stop
						</button>
					{:else}
						<button
							class="primary"
							disabled={busy.has(server.id)}
							onclick={() => act(server.id, api.startServer)}
						>
							Start
						</button>
					{/if}
					<button class="danger" disabled={busy.has(server.id)} onclick={() => remove(server)}>
						Delete
					</button>
				</div>
			</li>
		{/each}
	</ul>
{/if}

<style>
	.list {
		list-style: none;
		padding: 0;
		display: grid;
		gap: 0.75rem;
	}

	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		flex-wrap: wrap;
	}

	.title {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		font-weight: 600;
	}

	.title a {
		text-decoration: none;
		color: var(--text);
	}

	.address {
		margin-top: 0.35rem;
		color: var(--accent);
		font-size: 0.95rem;
	}

	.err {
		color: var(--danger);
	}

	.actions {
		display: flex;
		gap: 0.5rem;
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

	.empty {
		text-align: center;
		display: grid;
		gap: 0.75rem;
		justify-items: center;
	}
</style>
