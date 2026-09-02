import { api, ApiError, type User } from './api';

/**
 * Who is logged in, shared across routes. A Svelte 5 rune-backed module so any
 * component can read `session.user` reactively without prop drilling.
 */
class Session {
	user = $state<User | null>(null);
	/** True until the first /auth/me call settles, so the UI can avoid
	 *  flashing the login screen at an already-authenticated user. */
	loading = $state(true);

	async refresh(): Promise<void> {
		try {
			this.user = await api.me();
		} catch (err) {
			// A 401 is the normal "not logged in" answer, not a failure.
			if (!(err instanceof ApiError && err.status === 401)) {
				console.error('session refresh failed', err);
			}
			this.user = null;
		} finally {
			this.loading = false;
		}
	}

	async logout(): Promise<void> {
		await api.logout();
		this.user = null;
	}
}

export const session = new Session();
