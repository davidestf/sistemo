<script lang="ts">
  import { clearToken, getUsername } from '$lib/stores/auth.svelte';

  let hash = $state(window.location.hash || '#/');

  $effect(() => {
    const onHashChange = () => { hash = window.location.hash || '#/'; };
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  });

  function navigate(path: string) {
    window.location.hash = path;
  }

  function isActive(path: string): boolean {
    if (path === '#/') return hash === '#/' || hash === '#';
    return hash.startsWith(path);
  }

  let username = $derived(getUsername());

  const navItems = [
    { path: '#/', label: 'Dashboard', icon: 'home' },
    { path: '#/machines', label: 'Machines', icon: 'server' },
    { path: '#/images', label: 'Images', icon: 'image' },
    { path: '#/volumes', label: 'Volumes', icon: 'volume' },
    { path: '#/networks', label: 'Networks', icon: 'network' },
    { path: '#/history', label: 'History', icon: 'history' },
    { path: '#/system', label: 'System', icon: 'system' },
  ];
</script>

<aside class="w-60 bg-surface border-r border-border flex flex-col shrink-0">
  <div class="h-14 flex items-center px-5 border-b border-border">
    <button onclick={() => navigate('#/')} class="text-lg font-semibold text-text tracking-tight cursor-pointer bg-transparent border-none p-0">
      Sistemo
    </button>
  </div>

  <nav class="flex-1 p-3 flex flex-col gap-0.5">
    {#each navItems as item}
      <button
        onclick={() => navigate(item.path)}
        class="flex items-center gap-3 px-3 py-2 rounded-lg text-sm w-full text-left border-none cursor-pointer transition-colors
          {isActive(item.path) ? 'bg-surface-hover text-text' : 'bg-transparent text-muted hover:bg-surface-hover hover:text-text'}"
      >
        <svg class="w-4.5 h-4.5 shrink-0" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
          {#if item.icon === 'home'}
            <path d="M3 10.5L10 4l7 6.5V17a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1z" />
            <path d="M8 18V12h4v6" />
          {:else if item.icon === 'server'}
            <rect x="3" y="3" width="14" height="5" rx="1" />
            <rect x="3" y="12" width="14" height="5" rx="1" />
            <circle cx="6" cy="5.5" r="0.75" fill="currentColor" stroke="none" />
            <circle cx="6" cy="14.5" r="0.75" fill="currentColor" stroke="none" />
          {:else if item.icon === 'network'}
            <circle cx="10" cy="4" r="2" />
            <circle cx="4" cy="16" r="2" />
            <circle cx="16" cy="16" r="2" />
            <path d="M10 6v4m0 0l-5 4m5-4l5 4" />
          {:else if item.icon === 'volume'}
            <rect x="3" y="4" width="14" height="4" rx="1" />
            <rect x="3" y="10" width="14" height="4" rx="1" />
            <path d="M6 16v2m8-2v2" />
          {:else if item.icon === 'image'}
            <rect x="3" y="4" width="14" height="12" rx="1.5" />
            <path d="M3 13l4-4 3 3 4-5 3 3.5" />
            <circle cx="7" cy="8" r="1.5" fill="currentColor" stroke="none" />
          {:else if item.icon === 'history'}
            <circle cx="10" cy="10" r="7" />
            <path d="M10 6v4.5l3 2" />
            <path d="M3 10A7 7 0 0 1 10 3" stroke-dasharray="2 2" />
          {:else if item.icon === 'system'}
            <path d="M12 2a1 1 0 0 1 1 1v1.26a7 7 0 0 1 2.05 1.18l1.09-.63a1 1 0 0 1 1.36.37l1 1.73a1 1 0 0 1-.37 1.36l-1.09.63a7 7 0 0 1 0 2.36l1.09.63a1 1 0 0 1 .37 1.36l-1 1.73a1 1 0 0 1-1.36.37l-1.09-.63A7 7 0 0 1 13 15.74V17a1 1 0 0 1-1 1H8a1 1 0 0 1-1-1v-1.26a7 7 0 0 1-2.05-1.18l-1.09.63a1 1 0 0 1-1.36-.37l-1-1.73a1 1 0 0 1 .37-1.36l1.09-.63a7 7 0 0 1 0-2.36l-1.09-.63a1 1 0 0 1-.37-1.36l1-1.73a1 1 0 0 1 1.36-.37l1.09.63A7 7 0 0 1 7 4.26V3a1 1 0 0 1 1-1z" />
            <circle cx="10" cy="10" r="3" />
          {/if}
        </svg>
        {item.label}
      </button>
    {/each}
  </nav>

  <div class="p-3 border-t border-border">
    {#if username && username !== 'local'}
      <div class="flex items-center justify-between px-3 py-2">
        <span class="text-xs text-muted truncate">{username}</span>
        <button
          onclick={() => clearToken()}
          class="text-xs text-muted hover:text-error cursor-pointer bg-transparent border-none transition-colors"
        >
          Logout
        </button>
      </div>
    {:else}
      <div class="px-3 py-2 text-xs text-muted">Self-Hosted</div>
    {/if}
  </div>
</aside>
