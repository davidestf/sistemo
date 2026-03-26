import { getToken, clearToken } from '../stores/auth.svelte';

export class ApiError extends Error {
  status: number;
  body: string;

  constructor(status: number, body: string) {
    super(`API error ${status}: ${body}`);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
  }
}

async function request<T>(method: string, path: string, body?: unknown, retry = false): Promise<T> {
  const headers: Record<string, string> = {};

  // Only set Content-Type when sending a body
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
  }

  // Add JWT token if available
  const token = getToken();
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const opts: RequestInit = { method, headers };
  if (body !== undefined) {
    opts.body = JSON.stringify(body);
  }

  let response: Response;
  try {
    response = await fetch(path, opts);
  } catch (err) {
    if (retry) {
      response = await fetch(path, opts);
    } else {
      throw err;
    }
  }

  // Handle 401: clear token and force redirect to login
  if (response.status === 401) {
    clearToken();
    window.location.hash = '#/';
    throw new ApiError(401, 'Session expired. Please log in again.');
  }

  if (!response.ok) {
    const text = await response.text();
    // Try to extract structured error message from JSON response
    try {
      const parsed = JSON.parse(text);
      if (parsed.error) throw new ApiError(response.status, parsed.error);
    } catch (e) {
      if (e instanceof ApiError) throw e;
      // not JSON — fall through to raw text
    }
    throw new ApiError(response.status, text);
  }

  if (response.status === 204) return {} as T;

  const text = await response.text();
  if (!text) return {} as T;
  try {
    return JSON.parse(text) as T;
  } catch {
    throw new ApiError(response.status, 'Invalid JSON response from server');
  }
}

export function get<T>(path: string): Promise<T> {
  return request<T>('GET', path, undefined, true);
}

export function post<T>(path: string, body?: unknown): Promise<T> {
  return request<T>('POST', path, body);
}

export function del<T>(path: string): Promise<T> {
  return request<T>('DELETE', path);
}
