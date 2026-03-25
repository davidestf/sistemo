<script lang="ts">
  import type { AuditEntry } from '$lib/api/types';
  import { get } from '$lib/api/client';
  import { formatDate } from '$lib/utils/format';
  import Card from '$lib/components/ui/Card.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import EmptyState from '$lib/components/ui/EmptyState.svelte';

  const LIMIT = 50;
  const actionFilters = [
    'All Actions',
    'create',
    'delete',
    'start',
    'stop',
    'expose',
    'unexpose',
    'attach',
    'detach',
    'admin.login',
    'admin.setup',
    'image.delete',
  ];

  let entries = $state<AuditEntry[]>([]);
  let total = $state(0);
  let offset = $state(0);
  let filter = $state('All Actions');
  let loading = $state(true);
  let loadingMore = $state(false);
  let error = $state<string | undefined>();

  let hasMore = $derived(total > offset + LIMIT);

  async function fetchEntries(append = false) {
    if (append) {
      loadingMore = true;
    } else {
      loading = true;
    }
    error = undefined;

    try {
      let url = `/api/v1/audit-log?limit=${LIMIT}&offset=${append ? offset + LIMIT : 0}`;
      if (filter !== 'All Actions') {
        url += `&action=${encodeURIComponent(filter)}`;
      }

      const data = await get<{ entries: AuditEntry[]; total: number }>(url);
      if (append) {
        entries = [...entries, ...(data.entries ?? [])];
        offset = offset + LIMIT;
      } else {
        entries = data.entries ?? [];
        offset = 0;
      }
      total = data.total ?? 0;
    } catch (err) {
      error = err instanceof Error ? err.message : 'Failed to load history';
    } finally {
      loading = false;
      loadingMore = false;
    }
  }

  $effect(() => {
    // Re-fetch when filter changes
    const _f = filter;
    fetchEntries();
  });

  function truncate(text: string, max: number): string {
    if (!text || text.length <= max) return text || '';
    return text.slice(0, max) + '...';
  }
</script>

<div class="space-y-6">
  <div class="flex items-center justify-between">
    <h2 class="text-lg font-semibold text-text">History</h2>
    <select
      bind:value={filter}
      class="px-3 py-1.5 bg-surface-inner border border-border rounded-lg text-sm text-text focus:outline-none focus:border-accent"
    >
      {#each actionFilters as opt}
        <option value={opt}>{opt}</option>
      {/each}
    </select>
  </div>

  {#if loading}
    <div class="flex items-center justify-center py-20">
      <Spinner />
    </div>
  {:else if error}
    <div class="flex flex-col items-center gap-3 py-20">
      <p class="text-error text-sm">{error}</p>
      <button onclick={() => fetchEntries()} class="text-accent text-sm hover:underline cursor-pointer bg-transparent border-none">Retry</button>
    </div>
  {:else if entries.length === 0}
    <EmptyState message="No audit log entries found." />
  {:else}
    <Card padding={false}>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-border text-left">
              <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Time</th>
              <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Action</th>
              <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Target</th>
              <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Details</th>
              <th class="px-5 py-3 text-xs font-medium text-muted uppercase tracking-wider">Status</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            {#each entries as entry (entry.id)}
              <tr class="hover:bg-surface-hover/50 transition-colors">
                <td class="px-5 py-3 text-muted whitespace-nowrap">{formatDate(entry.timestamp)}</td>
                <td class="px-5 py-3 text-text">{entry.action}</td>
                <td class="px-5 py-3">
                  {#if entry.target_type === 'vm' && entry.target_id}
                    <a
                      href="#/vms/{entry.target_id}"
                      class="text-accent hover:underline font-medium"
                    >
                      {entry.target_name || entry.target_id}
                    </a>
                  {:else}
                    <span class="text-text">{entry.target_name || '—'}</span>
                  {/if}
                </td>
                <td class="px-5 py-3 text-muted max-w-xs" title={entry.details || ''}>
                  {truncate(entry.details, 80)}
                </td>
                <td class="px-5 py-3">
                  <span class="inline-block w-2.5 h-2.5 rounded-full {entry.success ? 'bg-success' : 'bg-error'}"></span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Card>

    {#if hasMore}
      <div class="flex justify-center pt-2">
        <Button variant="secondary" loading={loadingMore} onclick={() => fetchEntries(true)}>
          Load More
        </Button>
      </div>
    {/if}
  {/if}
</div>
