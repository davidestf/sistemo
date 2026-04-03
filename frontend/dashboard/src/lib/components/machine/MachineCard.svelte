<script lang="ts">
  import type { Machine } from '../../api/types';
  import { post, del } from '../../api/client';
  import { imageName } from '../../utils/format';
  import { addToast } from '../../stores/toast.svelte';
  import MachineStatusDot from './MachineStatusDot.svelte';
  import Button from '../ui/Button.svelte';

  let { machine, onrefresh }: { machine: Machine; onrefresh?: () => void } = $props();

  const isRunning = $derived(machine.status === 'running');

  function navigate() {
    window.location.hash = `/machines/${machine.id}`;
  }

  async function handleStop(e: MouseEvent) {
    e.stopPropagation();
    try {
      await post(`/api/v1/machines/${machine.id}/stop`);
      addToast(`"${machine.name}" stopped`, 'success');
      onrefresh?.();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to stop', 'error');
    }
  }

  async function handleStart(e: MouseEvent) {
    e.stopPropagation();
    try {
      await post(`/api/v1/machines/${machine.id}/start`);
      addToast(`"${machine.name}" started`, 'success');
      onrefresh?.();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to start', 'error');
    }
  }

  async function handleDelete(e: MouseEvent) {
    e.stopPropagation();
    if (!confirm(`Delete "${machine.name}"? This cannot be undone.`)) return;
    try {
      await del(`/api/v1/machines/${machine.id}`);
      addToast(`"${machine.name}" deleted`, 'success');
      onrefresh?.();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to delete', 'error');
    }
  }

  function openTerminal(e: MouseEvent) {
    e.stopPropagation();
    window.location.hash = `/machines/${machine.id}`;
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
<div
  class="bg-surface rounded-xl border border-border p-4 hover:border-surface-inner transition cursor-pointer"
  onclick={navigate}
>
  <div class="flex items-center justify-between mb-3">
    <span class="font-medium text-text truncate">{machine.name}</span>
    <MachineStatusDot status={machine.status} />
  </div>

  <div class="space-y-1 mb-4">
    <p class="text-sm text-muted truncate">{imageName(machine.image)}</p>
    {#if machine.ip_address}
      <p class="text-sm font-mono text-accent">{machine.ip_address}</p>
    {/if}
  </div>

  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div class="flex items-center gap-2" onclick={(e: MouseEvent) => e.stopPropagation()}>
    {#if isRunning}
      <Button variant="secondary" size="sm" onclick={openTerminal}>Terminal</Button>
      <Button variant="secondary" size="sm" onclick={handleStop}>Stop</Button>
    {:else}
      <Button variant="secondary" size="sm" onclick={handleStart}>Start</Button>
    {/if}
    <Button variant="danger" size="sm" onclick={handleDelete}>Delete</Button>
  </div>
</div>
