// Thin typed client over the Go REST API. Every call goes to a same-origin
// /api path: in dev Vite proxies it to the backend, in production a reverse
// proxy does. That keeps the session cookie same-site everywhere.

export type ServerType = 'VANILLA' | 'FORGE' | 'FABRIC' | 'NEOFORGE';
export type ModpackProvider = '' | 'curseforge' | 'modrinth';
export type Status =
	| 'creating'
	| 'stopped'
	| 'starting'
	| 'running'
	| 'stopping'
	| 'error';

export interface User {
	id: string;
	username: string;
	isAdmin: boolean;
	createdAt: string;
}

/** A user as the admin panel sees them: the account plus what it owns. */
export interface AdminUser extends User {
	serverCount: number;
}

export interface MinecraftServer {
	id: string;
	name: string;
	ownerId: string;
	type: ServerType;
	version: string;
	modpackProvider?: ModpackProvider;
	modpackId?: string;
	port: number;
	memory: string;
	cpuLimit: number;
	status: Status;
	statusMessage?: string;
	containerId?: string;
	createdAt: string;
	updatedAt: string;
	lastActiveAt?: string;
	/** host:port players connect to. */
	address: string;
	/** Live Docker state, absent when the container is gone. */
	containerStatus?: string;
	/** Healthcheck result: "starting" while the world loads, then "healthy". */
	health?: string;
}

export interface CreateServerInput {
	name: string;
	type: ServerType;
	version: string;
	modpackProvider?: ModpackProvider;
	modpackId?: string;
	memory?: string;
	cpuLimit?: number;
}

/** An API error carrying the HTTP status, so callers can special-case 401. */
export class ApiError extends Error {
	constructor(
		public status: number,
		message: string
	) {
		super(message);
		this.name = 'ApiError';
	}
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
	const res = await fetch(`/api${path}`, {
		// The session lives in a cookie, so every call must send credentials.
		credentials: 'include',
		headers: init.body ? { 'Content-Type': 'application/json' } : {},
		...init
	});

	if (res.status === 204) return undefined as T;

	const text = await res.text();
	const body = text ? JSON.parse(text) : null;

	if (!res.ok) {
		throw new ApiError(res.status, body?.error ?? `request failed (${res.status})`);
	}
	return body as T;
}

export const api = {
	// --- auth ---
	me: () => request<User>('/auth/me'),

	login: (username: string, password: string) =>
		request<User>('/auth/login', {
			method: 'POST',
			body: JSON.stringify({ username, password })
		}),

	register: (username: string, password: string) =>
		request<User>('/auth/register', {
			method: 'POST',
			body: JSON.stringify({ username, password })
		}),

	logout: () => request<void>('/auth/logout', { method: 'POST' }),

	// --- admin ---
	// Server management for admins goes through the endpoints above: the API
	// already lets an admin act on anybody's server, so there is no parallel
	// set of admin server calls.

	adminUsers: () => request<AdminUser[]>('/admin/users'),

	adminCreateUser: (username: string, password: string, isAdmin = false) =>
		request<AdminUser>('/admin/users', {
			method: 'POST',
			body: JSON.stringify({ username, password, isAdmin })
		}),

	adminSetUserAdmin: (id: string, isAdmin: boolean) =>
		request<AdminUser>(`/admin/users/${id}`, {
			method: 'PATCH',
			body: JSON.stringify({ isAdmin })
		}),

	adminSetUserPassword: (id: string, password: string) =>
		request<AdminUser>(`/admin/users/${id}`, {
			method: 'PATCH',
			body: JSON.stringify({ password })
		}),

	/**
	 * Delete an account. The API refuses while the user still owns servers
	 * unless reassign is true, which transfers them to the calling admin —
	 * deleting outright would strand their containers and volumes.
	 */
	adminDeleteUser: (id: string, reassign = false) =>
		request<void>(`/admin/users/${id}?reassign=${reassign}`, { method: 'DELETE' }),

	// --- servers ---
	listServers: () => request<MinecraftServer[]>('/servers'),

	getServer: (id: string) => request<MinecraftServer>(`/servers/${id}`),

	createServer: (input: CreateServerInput) =>
		request<MinecraftServer>('/servers', {
			method: 'POST',
			body: JSON.stringify(input)
		}),

	renameServer: (id: string, name: string) =>
		request<MinecraftServer>(`/servers/${id}`, {
			method: 'PATCH',
			body: JSON.stringify({ name })
		}),

	startServer: (id: string) =>
		request<MinecraftServer>(`/servers/${id}/start`, { method: 'POST' }),

	stopServer: (id: string) =>
		request<MinecraftServer>(`/servers/${id}/stop`, { method: 'POST' }),

	/**
	 * Delete a server. World data survives unless deleteData is true, so an
	 * accidental delete can be undone by recreating against the same volume.
	 */
	deleteServer: (id: string, deleteData = false) =>
		request<void>(`/servers/${id}?deleteData=${deleteData}`, { method: 'DELETE' }),

	/** URL for the SSE console stream; open it with EventSource. */
	logStreamUrl: (id: string, tail = 200) => `/api/servers/${id}/logs?tail=${tail}`
};

/**
 * A server is only joinable once Docker reports it healthy. Until then the
 * world is still generating or the modpack is still downloading, which can
 * take several minutes.
 */
export function isJoinable(server: MinecraftServer): boolean {
	return server.containerStatus === 'running' && server.health !== 'starting';
}

/** Human-readable state combining the record status and live container health. */
export function displayStatus(server: MinecraftServer): string {
	if (server.status === 'error') return 'error';
	if (server.containerStatus === 'running') {
		return server.health === 'starting' ? 'starting' : 'running';
	}
	if (server.containerStatus === 'exited' || server.containerStatus === 'created') {
		return 'stopped';
	}
	return server.status;
}
