<script lang="ts">
  let { status }: { status: string } = $props();

  const classes = $derived.by(() => {
    switch (status) {
      case 'running':
      case 'online':
      case 'healthy':
        return 'bg-success/15 text-success';
      case 'degraded':
        return 'bg-warning/15 text-warning';
      case 'stopped':
        return 'bg-warning/15 text-warning';
      case 'attached':
        return 'bg-accent/15 text-accent';
      case 'error':
      case 'failed':
      case 'unhealthy':
        return 'bg-error/15 text-error';
      case 'maintenance':
        return 'bg-warning/15 text-warning';
      default:
        return 'bg-muted/15 text-muted';
    }
  });
</script>

<span class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium {classes}">
  {status}
</span>
