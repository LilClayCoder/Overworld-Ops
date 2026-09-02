<script lang="ts">
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { session } from '$lib/session.svelte';

	let username = $state('');
	let password = $state('');
	let mode = $state<'login' | 'register'>('login');
	let error = $state('');
	let submitting = $state(false);

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		submitting = true;
		error = '';

		try {
			session.user =
				mode === 'login'
					? await api.login(username, password)
					: await api.register(username, password);
			goto('/');
		} catch (err) {
			error = err instanceof Error ? err.message : 'something went wrong';
		} finally {
			submitting = false;
		}
	}
</script>

<div class="card form">
	<h1>{mode === 'login' ? 'Log in' : 'Create an account'}</h1>

	{#if error}
		<p class="error">{error}</p>
	{/if}

	<form onsubmit={submit}>
		<label>
			Username
			<input bind:value={username} autocomplete="username" required />
		</label>

		<label>
			Password
			<input
				type="password"
				bind:value={password}
				autocomplete={mode === 'login' ? 'current-password' : 'new-password'}
				required
			/>
		</label>

		<button class="primary" type="submit" disabled={submitting}>
			{mode === 'login' ? 'Log in' : 'Sign up'}
		</button>
	</form>

	<p class="muted switch">
		{#if mode === 'login'}
			No account?
			<button class="link" onclick={() => ((mode = 'register'), (error = ''))}>Sign up</button>
		{:else}
			Already have one?
			<button class="link" onclick={() => ((mode = 'login'), (error = ''))}>Log in</button>
		{/if}
	</p>
</div>

<style>
	.form {
		max-width: 380px;
		margin: 3rem auto;
	}

	h1 {
		margin-top: 0;
		font-size: 1.25rem;
	}

	.switch {
		margin-bottom: 0;
		margin-top: 1rem;
	}

	button.link {
		background: none;
		border: none;
		color: var(--accent);
		padding: 0;
		text-decoration: underline;
		cursor: pointer;
	}
</style>
