<script lang="ts">
  import type { Machine, SystemInfo, ImageInfo, VolumeInfo } from '$lib/api/types';
  import { get } from '$lib/api/client';
  import { formatMB } from '$lib/utils/format';
  import Card from '$lib/components/ui/Card.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';

  let machines = $state<Machine[]>([]);
  let system = $state<SystemInfo | null>(null);
  let imageCount = $state(0);
  let volumeCount = $state(0);
  let totalImageMB = $state(0);
  let totalVolumeMB = $state(0);
  let networkCount = $state(0);
  let loading = $state(true);
  let error = $state<string | undefined>();

  async function fetchAll() {
    try {
      error = undefined;
      const [machineData, sysData, imgData, volData, netData] = await Promise.all([
        get<{ machines: Machine[] }>('/api/v1/machines'),
        get<SystemInfo>('/api/v1/system'),
        get<{ images: ImageInfo[] }>('/api/v1/images'),
        get<{ volumes: VolumeInfo[] }>('/api/v1/volumes').catch(() => ({ volumes: [] })),
        get<{ networks: any[] }>('/api/v1/networks'),
      ]);
      machines = machineData.machines ?? [];
      system = sysData;
      const images = imgData.images ?? [];
      imageCount = images.length;
      totalImageMB = images.reduce((sum, i) => sum + (i.size_mb || 0), 0);
      const volumes = volData.volumes ?? [];
      volumeCount = volumes.length;
      totalVolumeMB = volumes.reduce((sum: number, v: VolumeInfo) => sum + (v.size_mb || 0), 0);
      networkCount = (netData.networks ?? []).length;
    } catch (err) {
      error = err instanceof Error ? err.message : 'Failed to load';
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    fetchAll();
    const interval = setInterval(fetchAll, 10000);
    return () => clearInterval(interval);
  });

  let runningMachines = $derived(machines.filter(v => v.status === 'running').length);
  let stoppedMachines = $derived(machines.filter(v => v.status === 'stopped').length);
</script>

{#if loading}
  <div class="flex items-center justify-center py-20">
    <Spinner />
  </div>
{:else if error}
  <div class="flex flex-col items-center gap-3 py-20">
    <p class="text-error text-sm">{error}</p>
    <button onclick={fetchAll} class="text-accent text-sm hover:underline cursor-pointer bg-transparent border-none">Retry</button>
  </div>
{:else}
  <!-- Resource Tiles (clickable) -->
  <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
    <div onclick={() => { window.location.hash = '#/machines'; }} class="cursor-pointer group">
      <Card>
        <div class="flex items-center justify-between">
          <p class="text-sm text-muted group-hover:text-text transition">Machines</p>
          <svg class="w-5 h-5 text-muted group-hover:text-accent transition" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.5">
            <rect x="3" y="3" width="14" height="5" rx="1" />
            <rect x="3" y="12" width="14" height="5" rx="1" />
          </svg>
        </div>
        <p class="text-3xl font-bold text-text mt-2">{machines.length}</p>
        <p class="text-xs text-muted mt-1">
          {#if runningMachines > 0}<span class="text-success">{runningMachines} running</span>{/if}
          {#if runningMachines > 0 && stoppedMachines > 0} · {/if}
          {#if stoppedMachines > 0}<span class="text-warning">{stoppedMachines} stopped</span>{/if}
          {#if runningMachines === 0 && stoppedMachines === 0}No machines yet{/if}
        </p>
      </Card>
    </div>

    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
    <div onclick={() => { window.location.hash = '#/images'; }} class="cursor-pointer group">
      <Card>
        <div class="flex items-center justify-between">
          <p class="text-sm text-muted group-hover:text-text transition">Images</p>
          <svg class="w-5 h-5 text-muted group-hover:text-accent transition" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.5">
            <rect x="2" y="3" width="16" height="14" rx="2" />
            <path d="M2 13l4-4 3 3 4-5 5 6" />
          </svg>
        </div>
        <p class="text-3xl font-bold text-text mt-2">{imageCount}</p>
        <p class="text-xs text-muted mt-1">{formatMB(totalImageMB)} total</p>
      </Card>
    </div>

    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
    <div onclick={() => { window.location.hash = '#/volumes'; }} class="cursor-pointer group">
      <Card>
        <div class="flex items-center justify-between">
          <p class="text-sm text-muted group-hover:text-text transition">Volumes</p>
          <svg class="w-5 h-5 text-muted group-hover:text-accent transition" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.5">
            <rect x="3" y="4" width="14" height="4" rx="1" />
            <rect x="3" y="10" width="14" height="4" rx="1" />
          </svg>
        </div>
        <p class="text-3xl font-bold text-text mt-2">{volumeCount}</p>
        <p class="text-xs text-muted mt-1">{formatMB(totalVolumeMB)} total</p>
      </Card>
    </div>

    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
    <div onclick={() => { window.location.hash = '#/networks'; }} class="cursor-pointer group">
      <Card>
        <div class="flex items-center justify-between">
          <p class="text-sm text-muted group-hover:text-text transition">Networks</p>
          <svg class="w-5 h-5 text-muted group-hover:text-accent transition" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.5">
            <circle cx="10" cy="4" r="2" />
            <circle cx="4" cy="16" r="2" />
            <circle cx="16" cy="16" r="2" />
            <path d="M10 6v4m0 0l-5 4m5-4l5 4" />
          </svg>
        </div>
        <p class="text-3xl font-bold text-text mt-2">{networkCount}</p>
        <p class="text-xs text-muted mt-1">{system?.stats.vcpus_allocated ?? 0} vCPU / {formatMB(system?.stats.memory_mb_allocated ?? 0)} allocated</p>
      </Card>
    </div>
  </div>

  <!-- Host Summary Bar -->
  {#if system?.host}
    <div class="mt-6">
      <Card>
        <div class="flex flex-wrap items-center gap-x-8 gap-y-2">
          <div class="flex items-center gap-2">
            <span class="w-2 h-2 rounded-full {system.health.status === 'healthy' ? 'bg-success' : 'bg-error'}"></span>
            <span class="text-sm text-text font-medium">{system.host.hostname}</span>
          </div>
          <span class="text-sm text-muted">{system.host.cpus} cores</span>
          <span class="text-sm text-muted">{formatMB(system.host.memory_mb)} RAM</span>
          <span class="text-sm text-muted">{system.host.kernel}</span>
          <div class="flex items-center gap-2 ml-auto">
            <span class="text-sm text-muted">{system.host.disk_used_gb}/{system.host.disk_total_gb} GB</span>
            <div class="w-24 bg-surface-inner rounded-full h-1.5">
              <div
                class="rounded-full h-1.5 transition-all {(system.host.disk_used_gb / system.host.disk_total_gb) > 0.9 ? 'bg-error' : 'bg-accent'}"
                style="width: {Math.min((system.host.disk_used_gb / system.host.disk_total_gb) * 100, 100)}%"
              ></div>
            </div>
          </div>
        </div>
      </Card>
    </div>
  {/if}
{/if}
