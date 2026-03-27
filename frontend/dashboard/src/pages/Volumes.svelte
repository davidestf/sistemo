<script lang="ts">
  import type { VolumeInfo, VM } from '$lib/api/types';
  import { get, post, del } from '$lib/api/client';
  import { addToast } from '$lib/stores/toast.svelte';
  import { formatMB, timeAgo } from '$lib/utils/format';
  import Card from '$lib/components/ui/Card.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import EmptyState from '$lib/components/ui/EmptyState.svelte';
  import Modal from '$lib/components/ui/Modal.svelte';

  let volumes = $state<VolumeInfo[]>([]);
  let vms = $state<VM[]>([]);
  let loading = $state(true);
  let error = $state<string | undefined>();

  // Create form
  let showCreate = $state(false);
  let newName = $state('');
  let newSizeMb = $state(1024);
  let creating = $state(false);

  // Delete
  let deleteTarget = $state<VolumeInfo | null>(null);
  let deleting = $state(false);

  // Resize
  let resizeTarget = $state<VolumeInfo | null>(null);
  let resizeMb = $state(0);
  let resizing = $state(false);

  // Attach
  let attachTarget = $state<VolumeInfo | null>(null);
  let attachVmId = $state('');
  let attaching = $state(false);

  // Detach
  let detachTarget = $state<VolumeInfo | null>(null);
  let detaching = $state(false);

  const sizeOptions = [
    { label: '1 GB', value: 1024 },
    { label: '2 GB', value: 2048 },
    { label: '5 GB', value: 5120 },
    { label: '10 GB', value: 10240 },
    { label: '20 GB', value: 20480 },
    { label: '50 GB', value: 51200 },
  ];

  // Show ALL volumes (including root — they're valuable storage users should see)
  let dataVolumes = $derived(volumes);

  // Stopped VMs for attach dropdown
  let stoppedVMs = $derived(vms.filter(v => v.status === 'stopped'));

  function statusToBadge(status: string): string {
    return status;
  }

  async function fetchData() {
    try {
      const [volData, vmData] = await Promise.all([
        get<{ volumes: VolumeInfo[] }>('/api/v1/volumes'),
        get<{ vms: VM[] }>('/api/v1/vms'),
      ]);
      volumes = volData.volumes ?? [];
      vms = vmData.vms ?? [];
      if (loading) loading = false;
    } catch (err) {
      if (loading) {
        error = err instanceof Error ? err.message : 'Failed to load volumes';
        loading = false;
      }
    }
  }

  $effect(() => {
    fetchData();
    const interval = setInterval(fetchData, 10000);
    return () => clearInterval(interval);
  });

  // --- Create ---
  async function handleCreate(e: SubmitEvent) {
    e.preventDefault();
    if (!newName.trim()) return;

    creating = true;
    try {
      await post('/api/v1/volumes', { name: newName.trim(), size_mb: newSizeMb });
      addToast(`Volume "${newName.trim()}" created`, 'success');
      newName = '';
      newSizeMb = 1024;
      showCreate = false;
      await fetchData();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to create volume', 'error');
    } finally {
      creating = false;
    }
  }

  // --- Delete ---
  function confirmDelete(vol: VolumeInfo) {
    deleteTarget = vol;
  }

  async function handleDelete() {
    if (!deleteTarget) return;
    deleting = true;
    try {
      await del(`/api/v1/volumes/${deleteTarget.id}`);
      addToast(`Volume "${deleteTarget.name}" deleted`, 'success');
      deleteTarget = null;
      await fetchData();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to delete volume', 'error');
    } finally {
      deleting = false;
    }
  }

  // --- Resize ---
  function openResize(vol: VolumeInfo) {
    resizeTarget = vol;
    resizeMb = vol.size_mb;
  }

  async function handleResize() {
    if (!resizeTarget) return;
    if (resizeMb <= resizeTarget.size_mb) {
      addToast('New size must be larger than current size', 'error');
      return;
    }
    resizing = true;
    try {
      await post(`/api/v1/volumes/${resizeTarget.id}/resize`, { size_mb: resizeMb });
      addToast(`Volume "${resizeTarget.name}" resized to ${formatMB(resizeMb)}`, 'success');
      resizeTarget = null;
      await fetchData();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to resize volume', 'error');
    } finally {
      resizing = false;
    }
  }

  // --- Attach ---
  function openAttach(vol: VolumeInfo) {
    attachTarget = vol;
    attachVmId = stoppedVMs.length > 0 ? stoppedVMs[0].id : '';
  }

  async function handleAttach() {
    if (!attachTarget || !attachVmId) return;
    attaching = true;
    try {
      await post(`/api/v1/vms/${attachVmId}/volume/attach`, { volume: attachTarget.id });
      addToast(`Volume "${attachTarget.name}" attached`, 'success');
      attachTarget = null;
      attachVmId = '';
      await fetchData();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to attach volume', 'error');
    } finally {
      attaching = false;
    }
  }

  // --- Detach ---
  function confirmDetach(vol: VolumeInfo) {
    detachTarget = vol;
  }

  async function handleDetach() {
    if (!detachTarget || !detachTarget.attached) return;
    detaching = true;
    try {
      await post(`/api/v1/vms/${detachTarget.attached}/volume/detach`, { volume: detachTarget.id });
      addToast(`Volume "${detachTarget.name}" detached`, 'success');
      detachTarget = null;
      await fetchData();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to detach volume', 'error');
    } finally {
      detaching = false;
    }
  }

  function vmName(vmId: string): string {
    const vm = vms.find(v => v.id === vmId);
    return vm ? vm.name : vmId.slice(0, 8);
  }
</script>

{#if loading}
  <div class="flex items-center justify-center py-20">
    <Spinner />
  </div>
{:else if error}
  <div class="flex flex-col items-center gap-3 py-20">
    <p class="text-error text-sm">{error}</p>
    <button onclick={fetchData} class="text-accent text-sm hover:underline cursor-pointer bg-transparent border-none">Retry</button>
  </div>
{:else}
  <!-- Header -->
  <div class="flex items-center justify-between mb-4">
    <h2 class="text-lg font-semibold text-text">Volumes</h2>
    <Button variant="primary" onclick={() => { showCreate = !showCreate; }}>
      {showCreate ? 'Cancel' : 'Create Volume'}
    </Button>
  </div>

  <!-- Create Form -->
  {#if showCreate}
    <Card>
      <form onsubmit={handleCreate} class="space-y-4">
        <div>
          <label for="vol-name" class="block text-xs text-muted mb-1">Name</label>
          <input
            id="vol-name"
            type="text"
            bind:value={newName}
            placeholder="my-volume"
            required
            class="w-full px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-text placeholder:text-muted/50 focus:outline-none focus:border-accent"
          />
        </div>
        <div>
          <label class="block text-xs text-muted mb-1">Size</label>
          <div class="flex flex-wrap gap-2">
            {#each sizeOptions as opt}
              <button
                type="button"
                onclick={() => { newSizeMb = opt.value; }}
                class="px-4 py-2 rounded-lg text-sm font-medium transition cursor-pointer border
                  {newSizeMb === opt.value
                    ? 'bg-accent/15 text-accent border-accent/30'
                    : 'bg-surface-inner text-muted border-border hover:text-text'}"
              >
                {opt.label}
              </button>
            {/each}
          </div>
        </div>
        <Button variant="primary" loading={creating}>Create Volume</Button>
      </form>
    </Card>
    <div class="mt-4"></div>
  {/if}

  <!-- Volumes Table -->
  {#if dataVolumes.length === 0}
    <EmptyState
      message="No volumes yet. Create one to persist data across VM restarts."
      action="Create Volume"
      onaction={() => { showCreate = true; }}
    />
  {:else}
    <Card padding={false}>
      <div class="overflow-x-auto">
        <table class="w-full">
          <thead>
            <tr class="border-b border-border">
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Name</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Size</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Status</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Attached To</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Role</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Created</th>
              <th class="text-right text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            {#each dataVolumes as vol (vol.id)}
              <tr class="hover:bg-surface-hover transition">
                <td class="px-4 py-3 font-medium text-text text-sm">{vol.name}</td>
                <td class="px-4 py-3 text-muted text-sm">{formatMB(vol.size_mb)}</td>
                <td class="px-4 py-3"><Badge status={statusToBadge(vol.status)} /></td>
                <td class="px-4 py-3 text-sm">
                  {#if vol.attached}
                    <a
                      href="#/vms/{vol.attached}"
                      class="text-accent hover:underline"
                      onclick={(e: MouseEvent) => e.stopPropagation()}
                    >
                      {vmName(vol.attached)}
                    </a>
                  {:else}
                    <span class="text-muted">-</span>
                  {/if}
                </td>
                <td class="px-4 py-3 text-muted text-sm">{vol.role}</td>
                <td class="px-4 py-3 text-muted text-sm">{timeAgo(vol.created)}</td>
                <td class="px-4 py-3 text-right">
                  <div class="flex items-center justify-end gap-2">
                    {#if vol.status === 'online'}
                      {#if vol.role === 'root'}
                        <Button variant="primary" size="sm" onclick={() => {
                          sessionStorage.setItem('sistemo_deploy_volume', vol.name);
                          window.location.hash = '#/vms/create';
                        }}>Deploy</Button>
                      {/if}
                      <Button variant="secondary" size="sm" onclick={() => openAttach(vol)}>Attach</Button>
                      <Button variant="secondary" size="sm" onclick={() => openResize(vol)}>Resize</Button>
                      <Button variant="danger" size="sm" onclick={() => confirmDelete(vol)}>Delete</Button>
                    {:else if vol.status === 'attached'}
                      <Button variant="secondary" size="sm" onclick={() => confirmDetach(vol)}>Detach</Button>
                    {/if}
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

<!-- Delete Modal -->
<Modal
  open={deleteTarget !== null}
  title="Delete Volume"
  danger
  confirmText="Delete"
  onclose={() => { deleteTarget = null; }}
  onconfirm={handleDelete}
>
  {#if deleteTarget}
    <p>Are you sure you want to delete volume <strong class="text-text">{deleteTarget.name}</strong> ({formatMB(deleteTarget.size_mb)})? This action cannot be undone.</p>
  {/if}
</Modal>

<!-- Resize Modal -->
<Modal
  open={resizeTarget !== null}
  title="Resize Volume"
  confirmText={resizing ? 'Resizing...' : 'Resize'}
  onclose={() => { resizeTarget = null; }}
  onconfirm={handleResize}
>
  {#if resizeTarget}
    <p class="mb-3">Current size: <strong class="text-text">{formatMB(resizeTarget.size_mb)}</strong></p>
    <label for="resize-input" class="block text-xs text-muted mb-1">New size (MB)</label>
    <input
      id="resize-input"
      type="number"
      bind:value={resizeMb}
      min={resizeTarget.size_mb + 1}
      step="1024"
      class="w-full px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-text focus:outline-none focus:border-accent"
    />
    <p class="text-xs text-muted mt-1">Volumes can only be grown, not shrunk.</p>
  {/if}
</Modal>

<!-- Attach Modal -->
<Modal
  open={attachTarget !== null}
  title="Attach Volume"
  confirmText={attaching ? 'Attaching...' : 'Attach'}
  onclose={() => { attachTarget = null; attachVmId = ''; }}
  onconfirm={handleAttach}
>
  {#if attachTarget}
    <p class="mb-3">Attach <strong class="text-text">{attachTarget.name}</strong> to a stopped VM:</p>
    {#if stoppedVMs.length === 0}
      <p class="text-warning text-sm">No stopped VMs available. Stop a VM first to attach a volume.</p>
    {:else}
      <select
        bind:value={attachVmId}
        class="w-full px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-text focus:outline-none focus:border-accent"
      >
        {#each stoppedVMs as vm}
          <option value={vm.id}>{vm.name}</option>
        {/each}
      </select>
    {/if}
  {/if}
</Modal>

<!-- Detach Modal -->
<Modal
  open={detachTarget !== null}
  title="Detach Volume"
  confirmText={detaching ? 'Detaching...' : 'Detach'}
  onclose={() => { detachTarget = null; }}
  onconfirm={handleDetach}
>
  {#if detachTarget}
    <p>Detach <strong class="text-text">{detachTarget.name}</strong> from <strong class="text-text">{vmName(detachTarget.attached ?? '')}</strong>?</p>
  {/if}
</Modal>
