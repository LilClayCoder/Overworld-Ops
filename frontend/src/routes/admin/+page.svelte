<script lang="ts">
	import {
		api,
		displayStatus,
		type AdminUser,
		type MinecraftServer
	} from '$lib/api';
	import { session } from '$lib/session.svelte';

	let users = $state<AdminUser[]>([]);
	let servers = $state<MinecraftServer[]>([]);
	let error = $state('');
	let notice = $state('');
	let loading = $state(true);
	/** IDs with a request in flight, so buttons can be disabled. */
	let busy = $state(new Set<string>());

	let showCreate = $state(false);
	let newUsername = $state('');
	let newPassword = $state('');
	let newIsAdmin = $state(false);

	/** Owner ID to username, so the server list can name who owns what. */
	let ownerNames = $derived(new Map(users.map((u) => [u.id, u.username])));

	async function load() {
		try {
			// Both lists come from the same page, so one failure should not
			// leave half of it rendered against stale data.
			[users, servers] = await Promise.all([api.adminUsers(), api.listServers()]);
			error = '';
		} catch (err) {
			error = err instanceof Error ? err.message : 'could not load admin data';
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		if (!session.user?.isAdmin) {
			loading = false;
			return;
		}
		load();

		// Same cadence as the dashboard: servers change state on their own as
		// containers boot, and the owner column has to keep up.
		const timer = setInterval(load, 5000);
		return () => clearInterval(timer);
	});

	/** Run fn with id marked busy, then refresh. Errors surface in the banner. */
	async function act(id: string, fn: () => Promise<unknown>, success = '') {
		busy = new Set(busy).add(id);
		notice = '';
		try {
			await fn();
			notice = success;
			error = '';
			await load();
		} catch (err) {
			error = err instanceof Error ? err.message : 'action failed';
		} finally {
			const next = new Set(busy);
			next.delete(id);
			busy = next;
		}
	}

	async function createUser(event: SubmitEvent) {
		event.preventDefault();
		await act(
			'new-user',
			async () => {
				await api.adminCreateUser(newUsername, newPassword, newIsAdmin);
			},
			`Created ${newUsername}.`
		);
		if (!error) {
			newUsername = '';
			newPassword = '';
			newIsAdmin = false;
			showCreate = false;
		}
	}

	function toggleAdmin(user: AdminUser) {
		const verb = user.isAdmin ? 'Remove admin access from' : 'Grant admin access to';
		if (!confirm(`${verb} ${user.username}?`)) return;
		return act(
			user.id,
			() => api.adminSetUserAdmin(user.id, !user.isAdmin),
			`${user.username} is now ${user.isAdmin ? 'a regular user' : 'an admin'}.`
		);
	}

	function resetPassword(user: AdminUser) {
		const password = prompt(`New password for ${user.username} (at least 8 characters):`);
		if (!password) return;
		return act(
			user.id,
			() => api.adminSetUserPassword(user.id, password),
			`Password updated for ${user.username} — send it to them out of band.`
		);
	}

	function removeUser(user: AdminUser) {
		if (!confirm(`Delete the account "${user.username}"?`)) return;

		// The API refuses to delete an owner outright, because the cascade
		// would drop their server records and leave the containers running.
		let reassign = false;
		if (user.serverCount > 0) {
			reassign = confirm(
				`${user.username} owns ${user.serverCount} server(s).\n\n` +
					'OK — transfer them to your account, then delete the user.\n' +
					'Cancel — stop, so you can delete those servers yourself first.'
			);
			if (!reassign) return;
		}

		return act(
			user.id,
			() => api.adminDeleteUser(user.id, reassign),
			`Deleted ${user.username}.`
		);
	}

	function removeServer(server: MinecraftServer) {
		if (!confirm(`Delete "${server.name}"? Its container will be removed.`)) return;

		// Asked separately, and defaulting to "keep", because losing a world by
		// misreading one dialog is not a recoverable mistake.
		const deleteData = confirm(
			'Also delete the world data permanently?\n\n' +
				'OK — delete the world too. This cannot be undone.\n' +
				'Cancel — keep the world volume so the server can be recreated.'
		);
		return act(server.id, () => api.deleteServer(server.id, deleteData), `Deleted ${server.name}.`);
	}

	function formatDate(iso: string): string {
		return new Date(iso).toLocaleDateString(undefined, {
			year: 'numeric',
			month: 'short',
			day: 'numeric'
		});
	}
</script>

<h1>Admin</h1>

{#if !session.user?.isAdmin}
	<div class="card">
		<p>You need admin access to view this page.</p>
		<p class="muted">
			Ask an existing admin to grant it, or seed one with OWO_ADMIN_USERNAME on a fresh database.
		</p>
	</div>
{:else}
	{#if error}
		<p class="error">{error}</p>
	{/if}
	{#if notice}
		<p class="notice">{notice}</p>
	{/if}

	<section>
		<div class="section-head">
			<h2>Accounts <span class="muted count">{users.length}</span></h2>
			<button onclick={() => (showCreate = !showCreate)}>
				{showCreate ? 'Cancel' : 'Add account'}
			</button>
		</div>

		{#if showCreate}
			<form class="card create" onsubmit={createUser}>
				<p class="muted">
					Creates the account without logging you out. Useful once registration is closed.
				</p>
				<div class="fields">
					<label>
						Username
						<input bind:value={newUsername} autocomplete="off" required />
					</label>
					<label>
						Password
						<input type="password" bind:value={newPassword} autocomplete="new-password" required />
					</label>
				</div>
				<label class="check">
					<input type="checkbox" bind:checked={newIsAdmin} />
					Make this account an admin
				</label>
				<button class="primary" type="submit" disabled={busy.has('new-user')}>Create account</button>
			</form>
		{/if}

		{#if loading}
			<p class="muted">Loading…</p>
		{:else}
			<ul class="list">
				{#each users as user (user.id)}
					<li class="card row">
						<div class="info">
							<div class="title">
								{user.username}
								{#if user.isAdmin}<span class="badge admin">admin</span>{/if}
								{#if user.id === session.user?.id}<span class="badge">you</span>{/if}
							</div>
							<div class="muted">
								{user.serverCount}
								{user.serverCount === 1 ? 'server' : 'servers'} · joined {formatDate(user.createdAt)}
							</div>
						</div>

						<div class="actions">
							<button disabled={busy.has(user.id)} onclick={() => resetPassword(user)}>
								Reset password
							</button>
							<button
								disabled={busy.has(user.id) || user.id === session.user?.id}
								title={user.id === session.user?.id
									? 'You cannot change your own admin access'
									: ''}
								onclick={() => toggleAdmin(user)}
							>
								{user.isAdmin ? 'Remove admin' : 'Make admin'}
							</button>
							<button
								class="danger"
								disabled={busy.has(user.id) || user.id === session.user?.id}
								title={user.id === session.user?.id ? 'You cannot delete your own account' : ''}
								onclick={() => removeUser(user)}
							>
								Delete
							</button>
						</div>
					</li>
				{/each}
			</ul>
		{/if}
	</section>

	<section>
		<div class="section-head">
			<h2>All servers <span class="muted count">{servers.length}</span></h2>
		</div>

		{#if loading}
			<p class="muted">Loading…</p>
		{:else if servers.length === 0}
			<div class="card"><p class="muted">Nobody has created a server yet.</p></div>
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
								owned by {ownerNames.get(server.ownerId) ?? 'unknown'} · {server.type}
								{server.version} · {server.memory} · port {server.port}
							</div>
						</div>

						<div class="actions">
							{#if serverState === 'running' || serverState === 'starting'}
								<button
									disabled={busy.has(server.id)}
									onclick={() => act(server.id, () => api.stopServer(server.id))}
								>
									Stop
								</button>
							{:else}
								<button
									class="primary"
									disabled={busy.has(server.id)}
									onclick={() => act(server.id, () => api.startServer(server.id))}
								>
									Start
								</button>
							{/if}
							<button
								class="danger"
								disabled={busy.has(server.id)}
								onclick={() => removeServer(server)}
							>
								Delete
							</button>
						</div>
					</li>
				{/each}
			</ul>
		{/if}
	</section>
{/if}

<style>
	section {
		margin-bottom: 2.5rem;
	}

	.section-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		margin-bottom: 0.9rem;
	}

	h2 {
		font-size: 1.05rem;
		margin: 0;
	}

	.count {
		font-weight: 400;
	}

	.list {
		list-style: none;
		padding: 0;
		margin: 0;
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

	.actions {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
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

	.badge.admin {
		color: var(--accent);
		border-color: var(--accent-dim);
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

	.create {
		margin-bottom: 0.9rem;
	}

	.fields {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0 1rem;
	}

	@media (max-width: 520px) {
		.fields {
			grid-template-columns: 1fr;
		}
	}

	.check {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.check input {
		width: auto;
	}

	.notice {
		color: var(--accent);
		background: rgba(90, 196, 125, 0.1);
		border: 1px solid var(--accent-dim);
		border-radius: 6px;
		padding: 0.6rem 0.8rem;
		margin-bottom: 1rem;
		font-size: 0.9rem;
	}
</style>
