<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    padding = true,
    children,
  }: {
    padding?: boolean;
    children?: Snippet;
  } = $props();
</script>

<div class="bg-surface rounded-xl border border-border {padding ? 'p-5' : ''}">
  {#if children}
    {@render children()}
  {/if}
</div>
