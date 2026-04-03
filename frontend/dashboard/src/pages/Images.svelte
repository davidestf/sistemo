<script lang="ts">
  import type { ImageInfo, BuildStatus } from '$lib/api/types';
  import { get, del } from '$lib/api/client';
  import { formatMB, timeAgo } from '$lib/utils/format';
  import { addToast } from '$lib/stores/toast.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import EmptyState from '$lib/components/ui/EmptyState.svelte';

  let images = $state<ImageInfo[]>([]);
  let builds = $state<BuildStatus[]>([]);
  let loading = $state(true);
  let error = $state<string | undefined>();

  // Delete confirmation
  let deleteTarget = $state<ImageInfo | null>(null);
  let deleting = $state(false);

  async function fetchImages() {
    try {
      const data = await get<{ images: ImageInfo[] }>('/api/v1/images');
      images = data.images ?? [];
    } catch {
      // Silently fail on poll
    }
  }

  async function fetchAll() {
    loading = true;
    error = undefined;
    try {
      const [imgData, buildData] = await Promise.all([
        get<{ images: ImageInfo[] }>('/api/v1/images'),
        get<{ builds: BuildStatus[] }>('/api/v1/images/builds'),
      ]);
      images = imgData.images ?? [];
      builds = buildData.builds ?? [];
    } catch (err) {
      error = err instanceof Error ? err.message : 'Failed to load images';
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    fetchAll();
    const interval = setInterval(fetchImages, 10000);
    return () => clearInterval(interval);
  });

  function sourceStyle(source: string): string {
    switch (source) {
      case 'registry':
        return 'bg-success/15 text-success border border-success/30';
      case 'docker build':
        return 'bg-accent/15 text-accent border border-accent/30';
      default:
        return 'bg-surface-hover text-muted border border-border';
    }
  }

  function buildStatusStyle(status: string): string {
    switch (status) {
      case 'building':
        return 'bg-accent/15 text-accent border border-accent/30';
      case 'complete':
        return 'bg-success/15 text-success border border-success/30';
      case 'error':
        return 'bg-error/15 text-error border border-error/30';
      default:
        return 'bg-surface-hover text-muted border border-border';
    }
  }

  async function confirmDelete() {
    if (!deleteTarget) return;
    deleting = true;
    try {
      await del(`/api/v1/images/${encodeURIComponent(deleteTarget.name)}`);
      addToast(`Image "${deleteTarget.name}" deleted`, 'success');
      deleteTarget = null;
      await fetchImages();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to delete image', 'error');
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
    <button onclick={fetchAll} class="text-accent text-sm hover:underline cursor-pointer bg-transparent border-none">Retry</button>
  </div>
{:else}
  <div class="space-y-6">
    <h2 class="text-lg font-semibold text-text">Images</h2>

    {#if images.length === 0}
      <EmptyState
        message="No images yet. Download from registry or build from Docker."
        action="Deploy VM"
        onaction={() => { window.location.hash = '#/machines/create'; }}
      />
    {:else}
      <Card padding={false}>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-border text-left">
                <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Name</th>
                <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Size</th>
                <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Digest</th>
                <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Source</th>
                <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Created</th>
                <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Actions</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border">
              {#each images as img (img.path)}
                <tr class="hover:bg-surface-hover/50 transition-colors">
                  <td class="px-5 py-3 font-medium text-text">{img.name}</td>
                  <td class="px-5 py-3 text-muted">{formatMB(img.size_mb)}</td>
                  <td class="px-5 py-3 text-muted font-mono text-xs">
                    {#if img.digest}
                      <span title="sha256:{img.digest}">sha256:{img.digest.slice(0, 12)}</span>
                    {:else}
                      <span class="text-muted/50">-</span>
                    {/if}
                  </td>
                  <td class="px-5 py-3">
                    <span class="inline-block px-2 py-0.5 rounded-full text-xs font-medium {sourceStyle(img.source)}">
                      {img.source}
                    </span>
                  </td>
                  <td class="px-5 py-3 text-muted">{timeAgo(img.created_at)}</td>
                  <td class="px-5 py-3">
                    <div class="flex items-center justify-end gap-2">
                      <Button variant="primary" size="sm" onclick={() => {
                        sessionStorage.setItem('sistemo_deploy_image', JSON.stringify({ path: img.path, name: img.name }));
                        window.location.hash = '#/machines/create';
                      }}>Deploy</Button>
                      <Button variant="danger" size="sm" onclick={() => { deleteTarget = img; }}>Delete</Button>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
    {/if}

    <!-- Build History -->
    <div>
      <h3 class="text-base font-semibold text-text mb-3">Build History</h3>
      {#if builds.length === 0}
        <p class="text-sm text-muted">No builds yet.</p>
      {:else}
        <Card padding={false}>
          <div class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b border-border text-left">
                  <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Image</th>
                  <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Status</th>
                  <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Message</th>
                  <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Started</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-border">
                {#each builds as build (build.id)}
                  <tr class="hover:bg-surface-hover/50 transition-colors">
                    <td class="px-5 py-3 font-medium text-text">{build.image}</td>
                    <td class="px-5 py-3">
                      <span class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-medium {buildStatusStyle(build.status)}">
                        {#if build.status === 'building'}
                          <span class="w-1.5 h-1.5 rounded-full bg-accent animate-pulse"></span>
                        {/if}
                        {build.status}
                      </span>
                    </td>
                    <td class="px-5 py-3 text-muted max-w-xs truncate">{build.message || '—'}</td>
                    <td class="px-5 py-3 text-muted">{timeAgo(build.started_at)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </Card>
      {/if}
    </div>
  </div>
{/if}

<!-- Delete Confirmation Modal -->
{#if deleteTarget}
  <div class="fixed inset-0 bg-black/40 z-50 flex items-center justify-center" role="dialog">
    <div class="bg-surface rounded-xl border border-border p-6 max-w-sm w-full mx-4">
      <h3 class="text-base font-semibold text-text mb-2">Delete Image</h3>
      <p class="text-sm text-muted mb-5">
        Are you sure you want to delete <span class="font-medium text-text">{deleteTarget.name}</span>? This will permanently remove the image file.
      </p>
      <div class="flex justify-end gap-3">
        <Button variant="secondary" size="sm" onclick={() => { deleteTarget = null; }}>Cancel</Button>
        <Button variant="danger" size="sm" loading={deleting} onclick={confirmDelete}>Delete</Button>
      </div>
    </div>
  </div>
{/if}
