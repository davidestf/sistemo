<script lang="ts">
  import type { Snippet } from 'svelte';
  import Sidebar from './Sidebar.svelte';

  let { title, children }: { title: string; children: Snippet } = $props();
</script>

<div class="flex h-screen bg-base overflow-hidden">
  <Sidebar />

  <div class="flex flex-col flex-1 min-w-0">
    <header class="h-14 bg-surface border-b border-border flex items-center px-6 shrink-0">
      <h1 class="text-sm font-medium text-text">{title}</h1>
    </header>

    <main class="flex-1 overflow-y-auto p-6">
      {@render children()}
    </main>
  </div>
</div>
