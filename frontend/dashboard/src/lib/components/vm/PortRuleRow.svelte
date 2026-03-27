<script lang="ts">
  import type { PortRule } from '../../api/types';
  import { del } from '../../api/client';
  import { addToast } from '../../stores/toast.svelte';
  import Button from '../ui/Button.svelte';

  let {
    rule,
    vmId,
    ondelete,
  }: {
    rule: PortRule;
    vmId: string;
    ondelete: () => void;
  } = $props();

  let deleting = $state(false);

  async function handleDelete() {
    deleting = true;
    try {
      await del(`/api/v1/vms/${vmId}/expose/${rule.host_port}`);
      addToast('Port rule removed', 'success');
      ondelete();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to remove port rule', 'error');
    } finally {
      deleting = false;
    }
  }
</script>

<tr class="border-b border-border last:border-b-0">
  <td class="py-3 px-4 text-sm font-mono text-text">{rule.host_port}</td>
  <td class="py-3 px-4 text-sm font-mono text-text">{rule.vm_port}</td>
  <td class="py-3 px-4 text-sm text-muted uppercase">{rule.protocol}</td>
  <td class="py-3 px-4 text-right">
    <Button variant="danger" size="sm" loading={deleting} onclick={handleDelete}>Delete</Button>
  </td>
</tr>
