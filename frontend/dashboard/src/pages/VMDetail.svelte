<script lang="ts">
  import type { VM, VolumeInfo } from '$lib/api/types';
  import { get, post, del } from '$lib/api/client';
  import { getToken } from '$lib/stores/auth.svelte';
  import { imageName, formatMB, formatDate, timeAgo } from '$lib/utils/format';
  import { addToast } from '$lib/stores/toast.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import Modal from '$lib/components/ui/Modal.svelte';
  import Terminal from '../components/terminal/Terminal.svelte';
  import PortRuleRow from '$lib/components/vm/PortRuleRow.svelte';
  import ExposePortForm from '$lib/components/vm/ExposePortForm.svelte';

  let { vmId }: { vmId: string } = $props();

  let vm = $state<VM | null>(null);
  let loading = $state(true);
  let error = $state<string | undefined>();
  let activeTab = $state<'terminal' | 'logs' | 'ports' | 'volumes' | 'config'>('terminal');
  let showDeleteModal = $state(false);
  let deleting = $state(false);
  let logs = $state('');
  let logsLoading = $state(false);
  let logsEl: HTMLPreElement | undefined = $state();
  let attachedVolumes = $state<VolumeInfo[]>([]);
  let availableVolumes = $state<VolumeInfo[]>([]);
  let detachingId = $state<string | null>(null);
  let attachingVol = $state(false);

  async function fetchVM() {
    try {
      const data = await get<VM>(`/api/v1/vms/${vmId}`);
      const wasNull = vm === null;
      vm = data;
      // Set default tab based on status on first load
      if (wasNull) {
        activeTab = vm.status === 'running' ? 'terminal' : 'config';
      }
    } catch (err) {
      if (loading) {
        error = err instanceof Error ? err.message : 'Failed to load VM';
      }
    } finally {
      if (loading) loading = false;
    }
  }

  async function fetchVolumes() {
    try {
      const data = await get<{ volumes: VolumeInfo[] }>('/api/v1/volumes');
      const all = data.volumes ?? [];
      attachedVolumes = all.filter(v => v.attached === vmId);
      availableVolumes = all.filter(v => v.status === 'online' && v.role === 'data');
    } catch (err) {
      // Only log on initial load; suppress during polling to avoid console spam
      if (loading) console.warn('Failed to fetch volumes:', err);
    }
  }

  async function handleAttach(volId: string) {
    attachingVol = true;
    try {
      await post(`/api/v1/vms/${vmId}/volume/attach`, { volume: volId });
      addToast('Volume attached', 'success');
      await fetchVolumes();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to attach volume', 'error');
    } finally {
      attachingVol = false;
    }
  }

  async function handleDetach(vol: VolumeInfo) {
    detachingId = vol.id;
    try {
      await post(`/api/v1/vms/${vmId}/volume/detach`, { volume: vol.id });
      addToast(`Volume "${vol.name}" detached`, 'success');
      await fetchVolumes();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to detach volume', 'error');
    } finally {
      detachingId = null;
    }
  }

  $effect(() => {
    fetchVM();
    fetchVolumes();
    const interval = setInterval(() => { fetchVM(); fetchVolumes(); }, 5000);
    return () => clearInterval(interval);
  });

  // Logs fetching
  async function fetchLogs() {
    logsLoading = true;
    try {
      const headers: Record<string, string> = {};
      const token = getToken();
      if (token) headers['Authorization'] = `Bearer ${token}`;
      const response = await fetch(`/api/v1/vms/${vmId}/logs`, { headers });
      if (response.ok) {
        logs = await response.text();
      } else {
        logs = '';
      }
    } catch {
      logs = '';
    } finally {
      logsLoading = false;
    }
  }

  $effect(() => {
    if (activeTab === 'logs') {
      fetchLogs();
      const interval = setInterval(fetchLogs, 10000);
      return () => clearInterval(interval);
    }
  });

  // Auto-scroll logs
  $effect(() => {
    if (logs && logsEl) {
      logsEl.scrollTop = logsEl.scrollHeight;
    }
  });

  async function toggleVM() {
    if (!vm) return;
    const action = vm.status === 'running' ? 'stop' : 'start';
    try {
      await post(`/api/v1/vms/${vm.id}/${action}`);
      addToast(`VM ${action === 'stop' ? 'stopping' : 'starting'}...`, 'info');
      await fetchVM();
    } catch (err) {
      addToast(err instanceof Error ? err.message : `Failed to ${action} VM`, 'error');
    }
  }

  async function handleDelete() {
    if (!vm) return;
    deleting = true;
    try {
      await del(`/api/v1/vms/${vm.id}`);
      addToast(`VM "${vm.name}" deleted`, 'success');
      window.location.hash = '#/vms';
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to delete VM', 'error');
    } finally {
      deleting = false;
      showDeleteModal = false;
    }
  }

  function handlePortsChanged() {
    fetchVM();
  }

  const tabs: Array<{ key: typeof activeTab; label: string }> = [
    { key: 'terminal', label: 'Terminal' },
    { key: 'logs', label: 'Logs' },
    { key: 'ports', label: 'Ports' },
    { key: 'volumes', label: 'Volumes' },
    { key: 'config', label: 'Config' },
  ];
</script>

{#if loading}
  <div class="flex items-center justify-center py-20">
    <Spinner />
  </div>
{:else if error}
  <div class="flex flex-col items-center gap-3 py-20">
    <p class="text-error text-sm">{error}</p>
    <a href="#/vms" class="text-accent text-sm hover:underline">Back to VMs</a>
  </div>
{:else if vm}
  <!-- Header -->
  <div class="flex items-start justify-between mb-6">
    <div>
      <div class="flex items-center gap-3 mb-2">
        <h2 class="text-2xl font-semibold text-text">{vm.name}</h2>
        <Badge status={vm.status} />
      </div>
      <div class="flex items-center gap-4 text-sm">
        {#if vm.ip_address}
          <span class="font-mono text-accent">{vm.ip_address}</span>
        {/if}
        <span class="text-muted">{vm.network_name}</span>
        <span class="text-muted">{timeAgo(vm.created_at)}</span>
      </div>
    </div>
    <div class="flex items-center gap-2">
      {#if vm.status === 'running'}
        <Button variant="secondary" onclick={toggleVM}>Stop</Button>
      {:else}
        <Button variant="primary" onclick={toggleVM}>Start</Button>
      {/if}
      <Button variant="danger" onclick={() => { showDeleteModal = true; }}>Delete</Button>
    </div>
  </div>

  <!-- Tab Bar -->
  <div class="flex gap-1 border-b border-border mb-6">
    {#each tabs as tab}
      <button
        onclick={() => { activeTab = tab.key; }}
        class="px-4 py-2.5 text-sm font-medium transition cursor-pointer bg-transparent border-none border-b-2
          {activeTab === tab.key
            ? 'border-accent text-text'
            : 'border-transparent text-muted hover:text-text'}
          {tab.key === 'terminal' && vm.status !== 'running' ? 'opacity-50' : ''}"
      >
        {tab.label}
      </button>
    {/each}
  </div>

  <!-- Tab Content -->
  {#if activeTab === 'terminal'}
    {#if vm.status === 'running'}
      <Terminal vmId={vm.id} />
    {:else}
      <Card>
        <div class="flex items-center justify-center py-12">
          <p class="text-muted text-sm">VM is not running. Start it to access the terminal.</p>
        </div>
      </Card>
    {/if}

  {:else if activeTab === 'logs'}
    {#if logsLoading && !logs}
      <div class="flex items-center justify-center py-12">
        <Spinner />
      </div>
    {:else if logs}
      <pre
        bind:this={logsEl}
        class="bg-terminal rounded-lg p-4 font-mono text-sm text-text overflow-auto max-h-96"
      >{logs}</pre>
    {:else}
      <Card>
        <div class="flex items-center justify-center py-12">
          <p class="text-muted text-sm">No logs available.</p>
        </div>
      </Card>
    {/if}

  {:else if activeTab === 'ports'}
    <Card padding={false}>
      {#if vm.port_rules && vm.port_rules.length > 0}
        <table class="w-full">
          <thead>
            <tr class="border-b border-border">
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Host Port</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">VM Port</th>
              <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Protocol</th>
              <th class="text-right text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Actions</th>
            </tr>
          </thead>
          <tbody>
            {#each vm.port_rules as rule (rule.host_port)}
              <PortRuleRow {rule} vmId={vm.id} ondelete={handlePortsChanged} />
            {/each}
          </tbody>
        </table>
      {:else}
        <div class="px-5 py-8 text-center">
          <p class="text-muted text-sm">No exposed ports.</p>
        </div>
      {/if}
    </Card>
    <ExposePortForm vmId={vm.id} onexposed={handlePortsChanged} />

  {:else if activeTab === 'volumes'}
    <div class="flex items-center justify-between mb-4">
      <h3 class="text-lg font-semibold text-text">Attached Volumes</h3>
      {#if vm.status !== 'running' && availableVolumes.length > 0}
        <div class="flex gap-2">
          <select
            id="attach-vol-select"
            class="px-3 py-1.5 bg-surface-inner border border-border rounded-lg text-sm text-text focus:outline-none focus:border-accent"
          >
            <option value="">Select volume...</option>
            {#each availableVolumes as vol}
              <option value={vol.id}>{vol.name} ({formatMB(vol.size_mb)})</option>
            {/each}
          </select>
          <Button variant="primary" size="sm" loading={attachingVol} onclick={() => {
            const sel = document.getElementById('attach-vol-select') as HTMLSelectElement;
            if (sel?.value) handleAttach(sel.value);
          }}>Attach</Button>
        </div>
      {/if}
    </div>
    {#if attachedVolumes.length === 0}
      <Card>
        <p class="text-sm text-muted text-center py-4">No volumes attached to this VM.</p>
      </Card>
    {:else}
      <Card padding={false}>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-border">
                <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-5 py-3">Name</th>
                <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Size</th>
                <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Status</th>
                <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Role</th>
                <th class="text-left text-xs font-medium text-muted uppercase tracking-wider px-4 py-3">Created</th>
                <th class="text-right text-xs font-medium text-muted uppercase tracking-wider px-5 py-3">Actions</th>
              </tr>
            </thead>
            <tbody>
              {#each attachedVolumes as vol (vol.id)}
                <tr class="border-b border-border last:border-0 hover:bg-surface-hover/50 transition-colors">
                  <td class="px-5 py-3 text-text font-medium">{vol.name}</td>
                  <td class="px-4 py-3 text-muted">{formatMB(vol.size_mb)}</td>
                  <td class="px-4 py-3"><Badge status={vol.status} /></td>
                  <td class="px-4 py-3 text-muted">{vol.role}</td>
                  <td class="px-4 py-3 text-muted">{timeAgo(vol.created)}</td>
                  <td class="px-5 py-3 text-right">
                    {#if vm.status !== 'running'}
                      <Button variant="secondary" size="sm" loading={detachingId === vol.id} onclick={() => handleDetach(vol)}>Detach</Button>
                    {:else}
                      <span class="text-xs text-muted">Stop VM to manage</span>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
    {/if}

  {:else if activeTab === 'config'}
    <Card>
      <div class="grid grid-cols-2 md:grid-cols-3 gap-6">
        <div>
          <p class="text-sm text-muted">vCPUs</p>
          <p class="text-text mt-0.5">{vm.vcpus}</p>
        </div>
        <div>
          <p class="text-sm text-muted">Memory</p>
          <p class="text-text mt-0.5">{formatMB(vm.memory_mb)}</p>
        </div>
        <div>
          <p class="text-sm text-muted">Storage</p>
          <p class="text-text mt-0.5">{formatMB(vm.storage_mb)}</p>
        </div>
        <div>
          <p class="text-sm text-muted">Image</p>
          <p class="text-text mt-0.5">{imageName(vm.image)}</p>
          {#if vm.image_digest}
            <p class="text-muted font-mono text-xs mt-0.5" title="sha256:{vm.image_digest}">sha256:{vm.image_digest.slice(0, 16)}...</p>
          {/if}
        </div>
        <div>
          <p class="text-sm text-muted">Namespace</p>
          <p class="text-text mt-0.5">{vm.namespace || '-'}</p>
        </div>
        <div>
          <p class="text-sm text-muted">Network</p>
          <p class="text-text mt-0.5">{vm.network_name}</p>
        </div>
        <div>
          <p class="text-sm text-muted">Created</p>
          <p class="text-text mt-0.5">{formatDate(vm.created_at)}</p>
        </div>
        <div>
          <p class="text-sm text-muted">VM ID</p>
          <p class="text-text mt-0.5 font-mono text-sm">{vm.id}</p>
        </div>
        {#if vm.pid}
          <div>
            <p class="text-sm text-muted">PID</p>
            <p class="text-text mt-0.5 font-mono text-sm">{vm.pid}</p>
          </div>
        {/if}
      </div>
    </Card>

  {/if}
{/if}

<Modal
  open={showDeleteModal}
  title="Delete VM"
  danger
  confirmText="Delete"
  onclose={() => { showDeleteModal = false; }}
  onconfirm={handleDelete}
>
  {#if vm}
    <p>Are you sure you want to delete <strong class="text-text">{vm.name}</strong>? This action cannot be undone. All data will be lost.</p>
  {/if}
</Modal>
