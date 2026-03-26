<script lang="ts">
  import type { Snippet } from 'svelte';
  import Button from './Button.svelte';

  let {
    open,
    title,
    onclose,
    onconfirm,
    confirmText = 'Confirm',
    danger = false,
    children,
  }: {
    open: boolean;
    title: string;
    onclose: () => void;
    onconfirm: () => void;
    confirmText?: string;
    danger?: boolean;
    children?: Snippet;
  } = $props();

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && open) onclose();
  }

  function onbackdrop(e: MouseEvent) {
    if (e.target === e.currentTarget) onclose();
  }
</script>

<svelte:window {onkeydown} />

{#if open}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div
    class="fixed inset-0 bg-black/50 z-50 flex items-center justify-center"
    onclick={onbackdrop}
  >
    <div class="bg-surface rounded-xl border border-border p-6 max-w-md w-full mx-4">
      <h2 class="text-lg font-semibold text-text">{title}</h2>

      {#if children}
        <div class="mt-3 text-sm text-muted">
          {@render children()}
        </div>
      {/if}

      <div class="flex justify-end gap-3 mt-6">
        <Button variant="secondary" onclick={onclose}>Cancel</Button>
        <Button variant={danger ? 'danger' : 'primary'} onclick={onconfirm}>{confirmText}</Button>
      </div>
    </div>
  </div>
{/if}
