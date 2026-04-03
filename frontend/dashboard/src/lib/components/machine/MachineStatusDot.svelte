<script lang="ts">
  let { status }: { status: string } = $props();

  const classes = $derived.by(() => {
    switch (status) {
      case 'running':
        return 'bg-success animate-pulse';
      case 'stopped':
        return 'bg-warning';
      case 'error':
      case 'failed':
      case 'unhealthy':
        return 'bg-error';
      case 'maintenance':
        return 'bg-accent animate-pulse';
      default:
        return 'bg-muted';
    }
  });
</script>

<span class="inline-block w-2.5 h-2.5 rounded-full {classes}"></span>
