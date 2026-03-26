<script lang="ts">
  import Button from './Button.svelte';

  let {
    message,
    action,
    onaction,
  }: {
    message: string;
    action?: string;
    onaction?: () => void;
  } = $props();
</script>

<div class="flex flex-col items-center gap-4 py-12 text-muted">
  <svg class="w-12 h-12" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
    <path stroke-linecap="round" stroke-linejoin="round" d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4" />
  </svg>
  <p class="text-sm">{message}</p>
  {#if action && onaction}
    <Button variant="primary" onclick={onaction}>{action}</Button>
  {/if}
</div>
