<script lang="ts">
  import type { Snippet } from 'svelte';
  import Button from './Button.svelte';

  let { children }: { children: Snippet } = $props();

  let error = $state<string | null>(null);

  function handleError(event: ErrorEvent) {
    error = event.message || 'An unexpected error occurred';
    event.preventDefault();
  }

  function handleRejection(event: PromiseRejectionEvent) {
    error = event.reason?.message || 'An unexpected error occurred';
    event.preventDefault();
  }

  function retry() {
    error = null;
    window.location.reload();
  }

  $effect(() => {
    window.addEventListener('error', handleError);
    window.addEventListener('unhandledrejection', handleRejection);
    return () => {
      window.removeEventListener('error', handleError);
      window.removeEventListener('unhandledrejection', handleRejection);
    };
  });
</script>

{#if error}
  <div class="flex flex-col items-center justify-center min-h-[400px] gap-4 px-4">
    <div class="text-center max-w-md">
      <h2 class="text-lg font-semibold text-text mb-2">Something went wrong</h2>
      <p class="text-sm text-muted mb-4">{error}</p>
      <Button variant="primary" onclick={retry}>Reload Dashboard</Button>
    </div>
  </div>
{:else}
  {@render children()}
{/if}
