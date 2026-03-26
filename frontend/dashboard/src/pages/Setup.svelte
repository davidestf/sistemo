<script lang="ts">
  import { setToken, setAuthenticated } from '$lib/stores/auth.svelte';
  import { addToast } from '$lib/stores/toast.svelte';

  let username = $state('admin');
  let password = $state('');
  let confirmPassword = $state('');
  let loading = $state(false);
  let error = $state('');

  async function handleSetup(e: SubmitEvent) {
    e.preventDefault();
    if (!username || !password) return;

    if (password !== confirmPassword) {
      error = 'Passwords do not match';
      return;
    }
    if (password.length < 8) {
      error = 'Password must be at least 8 characters';
      return;
    }

    error = '';
    loading = true;
    try {
      const res = await fetch('/api/v1/auth/setup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      });
      const data = await res.json();

      if (!res.ok) {
        error = data.error || 'Setup failed';
        return;
      }

      if (data.token) {
        setToken(data.token);
        setAuthenticated(username);
        addToast('Admin account created', 'success');
      }
    } catch {
      error = 'Connection failed';
    } finally {
      loading = false;
    }
  }
</script>

<div class="min-h-screen bg-base flex items-center justify-center p-4">
  <div class="w-full max-w-sm">
    <div class="text-center mb-8">
      <h1 class="text-2xl font-bold text-text">Sistemo</h1>
      <p class="text-sm text-muted mt-1">Create your admin account</p>
    </div>

    <form onsubmit={handleSetup} class="bg-surface border border-border rounded-xl p-6 space-y-4">
      {#if error}
        <div class="text-sm text-error bg-error/10 border border-error/20 rounded-lg px-3 py-2">{error}</div>
      {/if}

      <div>
        <label for="username" class="block text-sm font-medium text-muted mb-1">Username</label>
        <input
          id="username"
          type="text"
          bind:value={username}
          autocomplete="username"
          required
          minlength={3}
          maxlength={64}
          class="w-full px-3 py-2 bg-surface-hover border border-border rounded-lg text-sm text-text focus:outline-none focus:border-accent"
        />
      </div>

      <div>
        <label for="password" class="block text-sm font-medium text-muted mb-1">Password</label>
        <input
          id="password"
          type="password"
          bind:value={password}
          autocomplete="new-password"
          required
          minlength={8}
          class="w-full px-3 py-2 bg-surface-hover border border-border rounded-lg text-sm text-text focus:outline-none focus:border-accent"
        />
        <p class="text-xs text-muted mt-1">Minimum 8 characters</p>
      </div>

      <div>
        <label for="confirm" class="block text-sm font-medium text-muted mb-1">Confirm Password</label>
        <input
          id="confirm"
          type="password"
          bind:value={confirmPassword}
          autocomplete="new-password"
          required
          class="w-full px-3 py-2 bg-surface-hover border border-border rounded-lg text-sm text-text focus:outline-none focus:border-accent"
        />
      </div>

      <button
        type="submit"
        disabled={loading}
        class="w-full py-2.5 bg-btn-primary hover:bg-btn-primary-hover text-white text-sm font-medium rounded-lg transition cursor-pointer disabled:opacity-50"
      >
        {loading ? 'Creating...' : 'Create Admin Account'}
      </button>
    </form>
  </div>
</div>
