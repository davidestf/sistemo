<script lang="ts">
  import type { SystemInfo } from '$lib/api/types';
  import { get } from '$lib/api/client';
  import { formatMB } from '$lib/utils/format';
  import Card from '$lib/components/ui/Card.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';

  let system = $state<SystemInfo | null>(null);
  let loading = $state(true);
  let error = $state<string | undefined>();

  async function fetchSystem() {
    try {
      system = await get<SystemInfo>('/api/v1/system');
      error = undefined;
      if (loading) loading = false;
    } catch (err) {
      if (loading) {
        error = err instanceof Error ? err.message : 'Failed to load system info';
        loading = false;
      }
    }
  }

  $effect(() => {
    fetchSystem();
    const interval = setInterval(fetchSystem, 10000);
    return () => clearInterval(interval);
  });
</script>

{#if loading}
  <div class="flex items-center justify-center py-20">
    <Spinner />
  </div>
{:else if error}
  <div class="flex flex-col items-center gap-3 py-20">
    <p class="text-error text-sm">{error}</p>
    <button onclick={fetchSystem} class="text-accent text-sm hover:underline cursor-pointer bg-transparent border-none">Retry</button>
  </div>
{:else if system}
  <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
    <!-- Host -->
    <Card>
      <h3 class="text-sm font-medium text-muted mb-4">Host</h3>
      <div class="space-y-3">
        {#if system.host?.hostname}
          <div>
            <p class="text-xs text-muted">Hostname</p>
            <p class="text-text text-sm mt-0.5 font-mono">{system.host.hostname}</p>
          </div>
        {/if}
        {#if system.host?.kernel}
          <div>
            <p class="text-xs text-muted">Kernel</p>
            <p class="text-text text-sm mt-0.5 font-mono">{system.host.kernel}</p>
          </div>
        {/if}
        {#if system.host?.cpus}
          <div>
            <p class="text-xs text-muted">CPU Cores</p>
            <p class="text-text text-sm mt-0.5">{system.host.cpus}</p>
          </div>
        {/if}
        {#if system.host?.memory_mb}
          <div>
            <p class="text-xs text-muted">Total Memory</p>
            <p class="text-text text-sm mt-0.5">{formatMB(system.host.memory_mb)}</p>
          </div>
        {/if}
        {#if system.host?.disk_total_gb}
          <div>
            <p class="text-xs text-muted">Disk</p>
            <p class="text-text text-sm mt-0.5">{system.host.disk_used_gb} GB used / {system.host.disk_total_gb} GB total ({system.host.disk_free_gb} GB free)</p>
            <div class="w-full bg-surface-inner rounded-full h-2 mt-1.5">
              <div
                class="bg-accent rounded-full h-2 transition-all"
                style="width: {Math.min((system.host.disk_used_gb / system.host.disk_total_gb) * 100, 100)}%"
              ></div>
            </div>
          </div>
        {/if}
      </div>
    </Card>

    <!-- Health -->
    <Card>
      <h3 class="text-sm font-medium text-muted mb-4">Health</h3>
      <div class="flex items-center gap-3 mb-4">
        <Badge status={system.health.status} />
      </div>
      <div class="space-y-2">
        <div class="flex items-center justify-between text-sm">
          <span class="text-muted">Firecracker</span>
          <span class={system.health.checks.firecracker ? 'text-success' : 'text-error'}>
            {system.health.checks.firecracker ? 'Available' : 'Not found'}
          </span>
        </div>
        <div class="flex items-center justify-between text-sm">
          <span class="text-muted">Kernel</span>
          <span class={system.health.checks.kernel ? 'text-success' : 'text-error'}>
            {system.health.checks.kernel ? 'Available' : 'Not found'}
          </span>
        </div>
      </div>
    </Card>

    <!-- Daemon -->
    <Card>
      <h3 class="text-sm font-medium text-muted mb-4">Daemon</h3>
      <div class="space-y-3">
        <div>
          <p class="text-xs text-muted">Architecture</p>
          <p class="text-text text-sm mt-0.5">{system.daemon.arch}</p>
        </div>
        <div>
          <p class="text-xs text-muted">Go Version</p>
          <p class="text-text text-sm mt-0.5">{system.daemon.go_version}</p>
        </div>
        <div>
          <p class="text-xs text-muted">Bridge</p>
          <p class="text-text text-sm mt-0.5 font-mono">{system.daemon.bridge}</p>
        </div>
        <div>
          <p class="text-xs text-muted">Goroutines</p>
          <p class="text-text text-sm mt-0.5">{system.daemon.goroutines}</p>
        </div>
      </div>
    </Card>

    <!-- VM Statistics -->
    <Card>
      <h3 class="text-sm font-medium text-muted mb-4">VM Statistics</h3>
      <div class="space-y-3">
        <div class="flex items-center justify-between text-sm">
          <span class="text-muted">Total VMs</span>
          <span class="text-text font-medium">{system.stats.total}</span>
        </div>
        <div class="flex items-center justify-between text-sm">
          <span class="text-muted">Running</span>
          <span class="text-success font-medium">{system.stats.running}</span>
        </div>
        <div class="flex items-center justify-between text-sm">
          <span class="text-muted">Stopped</span>
          <span class="text-warning font-medium">{system.stats.stopped}</span>
        </div>
        {#if system.stats.errored > 0}
          <div class="flex items-center justify-between text-sm">
            <span class="text-muted">Errored</span>
            <span class="text-error font-medium">{system.stats.errored}</span>
          </div>
        {/if}
        <div class="flex items-center justify-between text-sm">
          <span class="text-muted">vCPUs Allocated</span>
          <span class="text-text font-medium">{system.stats.vcpus_allocated}</span>
        </div>
        <div class="flex items-center justify-between text-sm">
          <span class="text-muted">Memory Allocated</span>
          <span class="text-text font-medium">{formatMB(system.stats.memory_mb_allocated)}</span>
        </div>
      </div>
    </Card>

    <!-- Limits -->
    <Card>
      <h3 class="text-sm font-medium text-muted mb-4">Limits</h3>
      <div class="space-y-3">
        <div>
          <p class="text-xs text-muted">Max vCPUs</p>
          <p class="text-text text-sm mt-0.5">{system.limits.max_vcpus}</p>
        </div>
        <div>
          <p class="text-xs text-muted">Max Memory</p>
          <p class="text-text text-sm mt-0.5">{formatMB(system.limits.max_memory_mb)}</p>
        </div>
        <div>
          <p class="text-xs text-muted">Max Storage</p>
          <p class="text-text text-sm mt-0.5">{formatMB(system.limits.max_storage_mb)}</p>
        </div>
      </div>
    </Card>
  </div>
{/if}
