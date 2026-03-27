<script lang="ts">
  import type { NetworkInfo } from '$lib/api/types';
  import { get, post, del } from '$lib/api/client';
  import { addToast } from '$lib/stores/toast.svelte';
  import { timeAgo } from '$lib/utils/format';
  import Card from '$lib/components/ui/Card.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import EmptyState from '$lib/components/ui/EmptyState.svelte';
  import Modal from '$lib/components/ui/Modal.svelte';

  let networks = $state<NetworkInfo[]>([]);
  let loading = $state(true);
  let error = $state<string | undefined>();

  // Create form
  let showCreate = $state(false);
  let newName = $state('');
  let newSubnet = $state('');
  let creating = $state(false);

  // Delete
  let deleteTarget = $state<NetworkInfo | null>(null);
  let deleting = $state(false);

  async function fetchNetworks() {
    try {
      const data = await get<{ networks: NetworkInfo[] }>('/api/v1/networks');
      networks = data.networks ?? [];
      error = undefined;
      if (loading) loading = false;
    } catch (err) {
      if (loading) {
        error = err instanceof Error ? err.message : 'Failed to load networks';
        loading = false;
      }
    }
  }

  $effect(() => {
    fetchNetworks();
    const interval = setInterval(fetchNetworks, 10000);
    return () => clearInterval(interval);
  });

  async function handleCreate(e: SubmitEvent) {
    e.preventDefault();
    if (!newName.trim()) return;

    creating = true;
    try {
      const body: Record<string, string> = { name: newName.trim() };
      if (newSubnet.trim()) body.subnet = newSubnet.trim();
      await post('/api/v1/networks', body);
      addToast(`Network "${newName.trim()}" created`, 'success');
      newName = '';
      newSubnet = '';
      showCreate = false;
      await fetchNetworks();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to create network', 'error');
    } finally {
      creating = false;
    }
  }

  function confirmDelete(net: NetworkInfo) {
    deleteTarget = net;
  }

  async function handleDelete() {
    if (!deleteTarget) return;
    deleting = true;
    try {
      await del(`/api/v1/networks/${deleteTarget.name}`);
      addToast(`Network "${deleteTarget.name}" deleted`, 'success');
      deleteTarget = null;
      await fetchNetworks();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to delete network', 'error');
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
    <button onclick={fetchNetworks} class="text-accent text-sm hover:underline cursor-pointer bg-transparent border-none">Retry</button>
  </div>
{:else}
  <!-- Header -->
  <div class="flex items-center justify-between mb-4">
    <h2 class="text-lg font-semibold text-text">Networks</h2>
    <Button variant="primary" onclick={() => { showCreate = !showCreate; }}>
      {showCreate ? 'Cancel' : 'Create Network'}
    </Button>
  </div>

  <!-- Create Form -->
  {#if showCreate}
    <Card>
      <form onsubmit={handleCreate} class="flex items-end gap-3">
        <div class="flex-1">
          <label for="net-name" class="block text-xs text-muted mb-1">Name</label>
          <input
            id="net-name"
            type="text"
            bind:value={newName}
            placeholder="my-network"
            required
            class="w-full px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-text placeholder:text-muted/50 focus:outline-none focus:border-accent"
          />
        </div>
        <div class="flex-1">
          <label for="net-subnet" class="block text-xs text-muted mb-1">Subnet (optional)</label>
          <input
            id="net-subnet"
            type="text"
            bind:value={newSubnet}
            placeholder="auto-assigned"
            class="w-full px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-text placeholder:text-muted/50 focus:outline-none focus:border-accent"
          />
        </div>
        <Button variant="primary" loading={creating}>Create</Button>
      </form>
    </Card>
    <div class="mt-4"></div>
  {/if}

  <!-- Networks Table -->
  {#if networks.length === 0}
    <EmptyState message="No networks configured." />
  {:else}
    <Card padding={false}>
      <div class="overflow-x-auto">
        <table class="w-full">
          <thead>
            <tr class="border-b border-border">
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Name</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Subnet</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Bridge</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">VMs</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Created</th>
              <th class="text-right text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            {#each networks as net (net.name)}
              <tr class="hover:bg-surface-hover transition">
                <td class="px-4 py-3 font-medium text-text text-sm">{net.name}</td>
                <td class="px-4 py-3 font-mono text-accent text-sm">{net.subnet}</td>
                <td class="px-4 py-3 text-muted text-sm">{net.bridge_name}</td>
                <td class="px-4 py-3 text-muted text-sm">{net.vm_count}</td>
                <td class="px-4 py-3 text-muted text-sm">{net.created_at ? timeAgo(net.created_at) : '-'}</td>
                <td class="px-4 py-3 text-right">
                  {#if net.name !== 'default'}
                    <Button variant="danger" size="sm" onclick={() => confirmDelete(net)}>Delete</Button>
                  {/if}
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
  title="Delete Network"
  danger
  confirmText="Delete"
  onclose={() => { deleteTarget = null; }}
  onconfirm={handleDelete}
>
  {#if deleteTarget}
    {#if deleteTarget.vm_count > 0}
      <p class="text-warning mb-2">Warning: This network has {deleteTarget.vm_count} VM{deleteTarget.vm_count === 1 ? '' : 's'} attached.</p>
    {/if}
    <p>Are you sure you want to delete network <strong class="text-text">{deleteTarget.name}</strong>?</p>
  {/if}
</Modal>
