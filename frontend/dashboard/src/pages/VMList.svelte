<script lang="ts">
  import type { VM } from '$lib/api/types';
  import { get, post, del } from '$lib/api/client';
  import { imageName, timeAgo } from '$lib/utils/format';
  import { addToast } from '$lib/stores/toast.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import EmptyState from '$lib/components/ui/EmptyState.svelte';
  import Modal from '$lib/components/ui/Modal.svelte';

  let vms = $state<VM[]>([]);
  let loading = $state(true);
  let error = $state<string | undefined>();
  let filter = $state<'all' | 'running' | 'stopped' | 'error'>('all');
  let deleteTarget = $state<VM | null>(null);
  let deleting = $state(false);

  const filters: Array<{ value: typeof filter; label: string }> = [
    { value: 'all', label: 'All' },
    { value: 'running', label: 'Running' },
    { value: 'stopped', label: 'Stopped' },
    { value: 'error', label: 'Error' },
  ];

  let filteredVMs = $derived.by(() => {
    if (filter === 'all') return vms;
    return vms.filter(v => v.status === filter);
  });

  async function fetchVMs() {
    try {
      const data = await get<{ vms: VM[] }>('/api/v1/vms');
      vms = data.vms ?? [];
      error = undefined;
      if (loading) loading = false;
    } catch (err) {
      if (loading) {
        error = err instanceof Error ? err.message : 'Failed to load VMs';
        loading = false;
      }
    }
  }

  $effect(() => {
    fetchVMs();
    const interval = setInterval(fetchVMs, 5000);
    return () => clearInterval(interval);
  });

  async function toggleVM(vm: VM, e: MouseEvent) {
    e.stopPropagation();
    const action = vm.status === 'running' ? 'stop' : 'start';
    try {
      await post(`/vms/${vm.id}/${action}`);
      addToast(`VM ${action === 'stop' ? 'stopping' : 'starting'}...`, 'info');
      await fetchVMs();
    } catch (err) {
      addToast(err instanceof Error ? err.message : `Failed to ${action} VM`, 'error');
    }
  }

  function openTerminal(vm: VM, e: MouseEvent) {
    e.stopPropagation();
    window.location.hash = `/vms/${vm.id}`;
  }

  function confirmDelete(vm: VM, e: MouseEvent) {
    e.stopPropagation();
    deleteTarget = vm;
  }

  async function handleDelete() {
    if (!deleteTarget) return;
    deleting = true;
    try {
      await del(`/vms/${deleteTarget.id}`);
      addToast(`VM "${deleteTarget.name}" deleted`, 'success');
      deleteTarget = null;
      await fetchVMs();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to delete VM', 'error');
    } finally {
      deleting = false;
    }
  }
</script>

{#if loading}
  <div class="flex items-center justify-center py-20">
    <Spinner />
  </div>
{:else if error}
  <div class="flex flex-col items-center gap-3 py-20">
    <p class="text-error text-sm">{error}</p>
    <button onclick={fetchVMs} class="text-accent text-sm hover:underline cursor-pointer bg-transparent border-none">Retry</button>
  </div>
{:else}
  <!-- Header -->
  <div class="flex items-center justify-between mb-4">
    <div class="flex items-center gap-4">
      <h2 class="text-lg font-semibold text-text">Virtual Machines</h2>
      <div class="flex gap-2">
        {#each filters as f}
          <button
            onclick={() => { filter = f.value; }}
            class="px-3 py-1 rounded-full text-xs font-medium transition cursor-pointer border-none
              {filter === f.value
                ? 'bg-accent/15 text-accent'
                : 'bg-surface-hover text-muted hover:text-text'}"
          >
            {f.label}
          </button>
        {/each}
      </div>
    </div>
    <Button variant="primary" onclick={() => { window.location.hash = '#/vms/create'; }}>Deploy VM</Button>
  </div>

  {#if filteredVMs.length === 0}
    {#if filter === 'all'}
      <EmptyState
        message="No VMs yet. Deploy your first one."
        action="Deploy VM"
        onaction={() => { window.location.hash = '#/vms/create'; }}
      />
    {:else}
      <EmptyState message="No {filter} VMs found." />
    {/if}
  {:else}
    <Card padding={false}>
      <div class="overflow-x-auto">
        <table class="w-full">
          <thead>
            <tr class="border-b border-border">
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Name</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Status</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Image</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">IP</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Network</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Created</th>
              <th class="text-right text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            {#each filteredVMs as vm (vm.id)}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <tr
                class="hover:bg-surface-hover transition cursor-pointer"
                onclick={() => { window.location.hash = `/vms/${vm.id}`; }}
                role="link"
                tabindex="0"
              >
                <td class="px-4 py-3 font-medium text-text text-sm">{vm.name}</td>
                <td class="px-4 py-3"><Badge status={vm.status} /></td>
                <td class="px-4 py-3 text-muted text-sm">{imageName(vm.image)}</td>
                <td class="px-4 py-3 font-mono text-accent text-sm">{vm.ip_address || '-'}</td>
                <td class="px-4 py-3 text-muted text-sm">{vm.network_name}</td>
                <td class="px-4 py-3 text-muted text-sm">{timeAgo(vm.created_at)}</td>
                <td class="px-4 py-3 text-right">
                  <div class="flex items-center justify-end gap-2">
                    {#if vm.status === 'running'}
                      <Button variant="secondary" size="sm" onclick={(e: MouseEvent) => openTerminal(vm, e)}>Terminal</Button>
                      <Button variant="secondary" size="sm" onclick={(e: MouseEvent) => toggleVM(vm, e)}>Stop</Button>
                    {:else}
                      <Button variant="secondary" size="sm" onclick={(e: MouseEvent) => toggleVM(vm, e)}>Start</Button>
                    {/if}
                    <Button variant="danger" size="sm" onclick={(e: MouseEvent) => confirmDelete(vm, e)}>Delete</Button>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Card>
  {/if}
{/if}

<Modal
  open={deleteTarget !== null}
  title="Delete VM"
  danger
  confirmText="Delete"
  onclose={() => { deleteTarget = null; }}
  onconfirm={handleDelete}
>
  {#if deleteTarget}
    <p>Are you sure you want to delete <strong class="text-text">{deleteTarget.name}</strong>? This action cannot be undone.</p>
  {/if}
</Modal>
