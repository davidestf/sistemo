<script lang="ts">
  import type { Snippet } from 'svelte';
  import Spinner from './Spinner.svelte';

  let {
    variant = 'primary',
    size = 'md',
    disabled = false,
    loading = false,
    onclick,
    children,
  }: {
    variant?: 'primary' | 'secondary' | 'danger';
    size?: 'sm' | 'md';
    disabled?: boolean;
    loading?: boolean;
    onclick?: (e: MouseEvent) => void;
    children?: Snippet;
  } = $props();

  const variantClass = $derived.by(() => {
    switch (variant) {
      case 'primary':
        return 'bg-btn-primary hover:bg-btn-primary-hover text-white';
      case 'secondary':
        return 'bg-surface-hover hover:bg-surface-inner border border-border text-text';
      case 'danger':
        return 'bg-error/15 hover:bg-error/25 text-error border border-error/30';
    }
  });

  const sizeClass = $derived(size === 'sm' ? 'px-3 py-1.5 text-xs' : 'px-4 py-2 text-sm');
</script>

<button
  class="inline-flex items-center justify-center gap-2 rounded-lg font-medium transition cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed {variantClass} {sizeClass}"
  disabled={disabled || loading}
  onclick={loading ? undefined : onclick}
>
  {#if loading}
    <Spinner size="sm" />
  {/if}
  {#if children}
    {@render children()}
  {/if}
</button>
