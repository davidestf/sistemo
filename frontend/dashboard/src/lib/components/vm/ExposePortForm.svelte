<script lang="ts">
  import { post } from '../../api/client';
  import { addToast } from '../../stores/toast.svelte';
  import Button from '../ui/Button.svelte';

  let {
    vmId,
    onexposed,
  }: {
    vmId: string;
    onexposed: () => void;
  } = $props();

  let hostPort = $state('');
  let vmPort = $state('');
  let submitting = $state(false);

  async function handleSubmit(e: SubmitEvent) {
    e.preventDefault();
    const vp = parseInt(vmPort, 10);
    if (!vp || vp < 1 || vp > 65535) {
      addToast('VM port is required (1-65535)', 'error');
      return;
    }

    // Auto-assign host port = vm port if not specified
    const hp = hostPort.trim() ? parseInt(hostPort, 10) : vp;
    if (hp < 1 || hp > 65535) {
      addToast('Host port must be 1-65535', 'error');
      return;
    }

    submitting = true;
    try {
      await post(`/api/v1/vms/${vmId}/expose`, {
        host_port: hp,
        vm_port: vp,
        protocol: 'tcp',
      });
      addToast(`Port ${hp} → ${vp}/tcp exposed`, 'success');
      hostPort = '';
      vmPort = '';
      onexposed();
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to expose port', 'error');
    } finally {
      submitting = false;
    }
  }
</script>

<form onsubmit={handleSubmit} class="flex items-end gap-3 mt-4">
  <div class="flex-1">
    <label for="host-port" class="block text-xs text-muted mb-1">Host Port</label>
    <input
      id="host-port"
      type="text"
      inputmode="numeric"
      bind:value={hostPort}
      placeholder="same as VM port"
      class="w-full px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-text placeholder:text-muted/50 focus:outline-none focus:border-accent"
    />
  </div>

  <div class="flex-1">
    <label for="vm-port" class="block text-xs text-muted mb-1">VM Port</label>
    <input
      id="vm-port"
      type="text"
      inputmode="numeric"
      bind:value={vmPort}
      placeholder="80"
      required
      class="w-full px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-text placeholder:text-muted/50 focus:outline-none focus:border-accent"
    />
  </div>

  <div class="w-20">
    <label class="block text-xs text-muted mb-1">Protocol</label>
    <div class="px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-muted">TCP</div>
  </div>

  <Button variant="primary" size="md" loading={submitting}>Expose</Button>
</form>
