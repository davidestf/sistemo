// Dashboard authentication state using Svelte 5 runes.

let authState = $state<'loading' | 'setup' | 'login' | 'authenticated'>('loading');
let username = $state<string>('');

export function getAuthState() { return authState; }
export function getUsername() { return username; }

export function setAuthenticated(user: string) {
  authState = 'authenticated';
  username = user;
}

export function setNeedsSetup() { authState = 'setup'; }
export function setNeedsLogin() { authState = 'login'; }
export function setLoading() { authState = 'loading'; }

export function getToken(): string | null {
  return sessionStorage.getItem('sistemo_token');
}

export function setToken(token: string) {
  sessionStorage.setItem('sistemo_token', token);
}

export function clearToken() {
  sessionStorage.removeItem('sistemo_token');
  authState = 'login';
  username = '';
}

// Check auth status with the backend. Called on page load.
export async function checkAuth(): Promise<void> {
  try {
    const headers: Record<string, string> = {};
    const token = getToken();
    if (token) {
      headers['Authorization'] = `Bearer ${token}`;
    }

    const res = await fetch('/api/v1/auth/status', { headers });
    const data = await res.json();

    if (data.setup_required) {
      setNeedsSetup();
    } else if (data.authenticated) {
      setAuthenticated(data.username);
    } else {
      setNeedsLogin();
    }
  } catch {
    // If auth endpoint is unreachable, require login — fail-closed
    setNeedsLogin();
  }
}
