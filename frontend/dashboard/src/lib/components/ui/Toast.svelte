<script lang="ts">
  import { getToasts } from '../../stores/toast.svelte';

  const toasts = $derived(getToasts());

  function borderColor(type: string): string {
    switch (type) {
      case 'success': return 'border-l-success';
      case 'error': return 'border-l-error';
      default: return 'border-l-accent';
    }
  }
</script>

{#if toasts.length > 0}
  <div class="fixed bottom-6 right-6 z-50 flex flex-col gap-2">
    {#each toasts as toast (toast.id)}
      <div class="bg-surface border border-border rounded-lg p-4 shadow-lg max-w-sm border-l-4 {borderColor(toast.type)}">
        <p class="text-sm text-text">{toast.message}</p>
      </div>
    {/each}
  </div>
{/if}
