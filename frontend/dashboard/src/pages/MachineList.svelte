<script lang="ts">
  import type { Machine } from '$lib/api/types';
  import { get, post, del } from '$lib/api/client';
  import { imageName, timeAgo } from '$lib/utils/format';
  import { addToast } from '$lib/stores/toast.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import EmptyState from '$lib/components/ui/EmptyState.svelte';
  import Modal from '$lib/components/ui/Modal.svelte';

  let machines = $state<Machine[]>([]);
  let loading = $state(true);
  let error = $state<string | undefined>();
  let filter = $state<'all' | 'running' | 'stopped' | 'error'>('all');
  let deleteTarget = $state<Machine | null>(null);
  let deleting = $state(false);

  const filters: Array<{ value: typeof filter; label: string }> = [
    { value: 'all', label: 'All' },
    { value: 'running', label: 'Running' },
    { value: 'stopped', label: 'Stopped' },
    { value: 'error', label: 'Error' },
  ];

  let filteredMachines = $derived.by(() => {
    if (filter === 'all') return machines;
    return machines.filter(v => v.status === filter);
  });

  async function fetchMachines() {
    try {
      const data = await get<{ machines: Machine[] }>('/api/v1/machines');
      machines = data.machines ?? [];
      error = undefined;
      if (loading) loading = false;
    } catch (err) {
      if (loading) {
        error = err instanceof Error ? err.message : 'Failed to load machines';
        loading = false;
      }
    }
  }

  $effect(() => {
    fetchMachines();
    const interval = setInterval(fetchMachines, 5000);
    return () => clearInterval(interval);
  });

  async function toggleMachine(machine: Machine, e: MouseEvent) {
    e.stopPropagation();
    const action = machine.status === 'running' ? 'stop' : 'start';
    try {
      await post(`/api/v1/machines/${machine.id}/${action}`);
      addToast(`Machine ${action === 'stop' ? 'stopping' : 'starting'}...`, 'info');
      await fetchMachines();
    } catch (err) {
      addToast(err instanceof Error ? err.message : `Failed to ${action} machine`, 'error');
    }
  }

  function openTerminal(machine: Machine, e: MouseEvent) {
    e.stopPropagation();
    window.location.hash = `/machines/${machine.id}`;
  }

  function confirmDelete(machine: Machine, e: MouseEvent) {
    e.stopPropagation();
    deleteTarget = machine;
  }

  async function handleDelete() {
    if (!deleteTarget) return;
    deleting = true;
    try {
      await del(`/api/v1/machines/${deleteTarget.id}`);
      addToast(`Machine "${deleteTarget.name}" deleted`, 'success');
      deleteTarget = null;
      await fetchMachines();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to delete machine', 'error');
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
    <button onclick={fetchMachines} class="text-accent text-sm hover:underline cursor-pointer bg-transparent border-none">Retry</button>
  </div>
{:else}
  <!-- Header -->
  <div class="flex items-center justify-between mb-4">
    <div class="flex items-center gap-4">
      <h2 class="text-lg font-semibold text-text">Machines</h2>
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
    <Button variant="primary" onclick={() => { window.location.hash = '#/machines/create'; }}>Deploy Machine</Button>
  </div>

  {#if filteredMachines.length === 0}
    {#if filter === 'all'}
      <EmptyState
        message="No machines yet. Deploy your first one."
        action="Deploy Machine"
        onaction={() => { window.location.hash = '#/machines/create'; }}
      />
    {:else}
      <EmptyState message="No {filter} machines found." />
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
            {#each filteredMachines as machine (machine.id)}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <tr
                class="hover:bg-surface-hover transition cursor-pointer"
                onclick={() => { window.location.hash = `/machines/${machine.id}`; }}
                role="link"
                tabindex="0"
              >
                <td class="px-4 py-3 font-medium text-text text-sm">{machine.name}</td>
                <td class="px-4 py-3"><Badge status={machine.status} /></td>
                <td class="px-4 py-3 text-muted text-sm">{imageName(machine.image)}</td>
                <td class="px-4 py-3 font-mono text-accent text-sm">{machine.ip_address || '-'}</td>
                <td class="px-4 py-3 text-muted text-sm">{machine.network_name}</td>
                <td class="px-4 py-3 text-muted text-sm">{timeAgo(machine.created_at)}</td>
                <td class="px-4 py-3 text-right">
                  <div class="flex items-center justify-end gap-2">
                    {#if machine.status === 'running'}
                      <Button variant="secondary" size="sm" onclick={(e: MouseEvent) => openTerminal(machine, e)}>Terminal</Button>
                      <Button variant="secondary" size="sm" onclick={(e: MouseEvent) => toggleMachine(machine, e)}>Stop</Button>
                    {:else}
                      <Button variant="secondary" size="sm" onclick={(e: MouseEvent) => toggleMachine(machine, e)}>Start</Button>
                    {/if}
                    <Button variant="danger" size="sm" onclick={(e: MouseEvent) => confirmDelete(machine, e)}>Delete</Button>
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
  title="Delete Machine"
  danger
  confirmText="Delete"
  onclose={() => { deleteTarget = null; }}
  onconfirm={handleDelete}
>
  {#if deleteTarget}
    <p>Are you sure you want to delete <strong class="text-text">{deleteTarget.name}</strong>? This action cannot be undone.</p>
  {/if}
</Modal>
