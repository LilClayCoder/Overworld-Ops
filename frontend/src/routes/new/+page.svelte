<script lang="ts">
	import { goto } from '$app/navigation';
	import { api, type CreateServerInput, type ModpackProvider, type ServerType } from '$lib/api';

	let name = $state('');
	let type = $state<ServerType>('VANILLA');
	let version = $state('LATEST');
	let modpackProvider = $state<ModpackProvider>('');
	let modpackId = $state('');
	let memory = $state('2G');
	let cpuLimit = $state(2);
	let error = $state('');
	let submitting = $state(false);

	// A modpack pins its own loader and Minecraft version, so those inputs are
	// hidden rather than sending values the backend would ignore.
	const usingModpack = $derived(modpackProvider !== '');

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		submitting = true;
		error = '';

		const input: CreateServerInput = {
			name,
			type,
			version,
			memory,
			cpuLimit
		};
		if (usingModpack) {
			input.modpackProvider = modpackProvider;
			input.modpackId = modpackId.trim();
		}

		try {
			const server = await api.createServer(input);
			goto(`/servers/${server.id}`);
		} catch (err) {
			error = err instanceof Error ? err.message : 'could not create server';
		} finally {
			submitting = false;
		}
	}
</script>

<h1>New server</h1>

{#if error}
	<p class="error">{error}</p>
{/if}

<form class="card" onsubmit={submit}>
	<label>
		Name
		<input bind:value={name} placeholder="Friday SMP" maxlength="64" required />
	</label>

	<label>
		Modpack
		<select bind:value={modpackProvider}>
			<option value="">None — plain server</option>
			<option value="modrinth">Modrinth</option>
			<option value="curseforge">CurseForge</option>
		</select>
	</label>

	{#if usingModpack}
		<label>
			{modpackProvider === 'modrinth' ? 'Modrinth project slug' : 'CurseForge modpack slug'}
			<input
				bind:value={modpackId}
				placeholder={modpackProvider === 'modrinth' ? 'cobblemon-official' : 'all-the-mods-10'}
				required
			/>
		</label>
		<p class="muted hint">
			The pack decides its own modloader and Minecraft version. First boot downloads it, which
			can take several minutes.
		</p>
	{:else}
		<label>
			Modloader
			<select bind:value={type}>
				<option value="VANILLA">Vanilla</option>
				<option value="FABRIC">Fabric</option>
				<option value="FORGE">Forge</option>
				<option value="NEOFORGE">NeoForge</option>
			</select>
		</label>

		<label>
			Minecraft version
			<input bind:value={version} placeholder="LATEST or 1.21.1" required />
		</label>
	{/if}

	<div class="two-up">
		<label>
			Memory (JVM heap)
			<select bind:value={memory}>
				<option value="2G">2 GB</option>
				<option value="4G">4 GB</option>
				<option value="6G">6 GB</option>
				<option value="8G">8 GB</option>
			</select>
		</label>

		<label>
			CPU cores
			<input type="number" bind:value={cpuLimit} min="0.5" max="16" step="0.5" />
		</label>
	</div>

	<p class="muted hint">
		Creating a server accepts the
		<a href="https://www.minecraft.net/eula" target="_blank" rel="noreferrer">Minecraft EULA</a>
		on your behalf.
	</p>

	<button class="primary" type="submit" disabled={submitting}>
		{submitting ? 'Creating…' : 'Create server'}
	</button>
</form>

<style>
	form {
		max-width: 520px;
	}

	.two-up {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 1rem;
	}

	.hint {
		margin-top: -0.4rem;
	}
</style>
