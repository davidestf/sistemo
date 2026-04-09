<script lang="ts">
  import { getToken } from '$lib/stores/auth.svelte';

  let { machineId }: { machineId: string } = $props();

  let containerEl: HTMLDivElement | undefined = $state();
  let error: string | undefined = $state();

  $effect(() => {
    if (!containerEl) return;

    let term: any;
    let ws: WebSocket | undefined;
    let fitAddon: any;
    let disposed = false;

    async function init() {
      try {
        const { Terminal } = await import('@xterm/xterm');
        const { FitAddon } = await import('@xterm/addon-fit');
        await import('@xterm/xterm/css/xterm.css');

        if (disposed) return;

        term = new Terminal({
          cursorBlink: true,
          fontSize: 14,
          fontFamily: 'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace',
          theme: {
            background: '#161b28',
            foreground: '#d1d5db',
            cursor: '#38bdf8',
            selectionBackground: '#2a3040',
            black: '#1a1f2e',
            red: '#f87171',
            green: '#10b981',
            yellow: '#fbbf24',
            blue: '#38bdf8',
            magenta: '#c084fc',
            cyan: '#22d3ee',
            white: '#d1d5db',
          },
        });

        fitAddon = new FitAddon();
        term.loadAddon(fitAddon);
        term.open(containerEl!);
        fitAddon.fit();

        const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
        const token = getToken();
        const wsUrl = `${protocol}//${location.host}/terminals/machine/${machineId}`;
        let reconnectAttempts = 0;
        const maxReconnectAttempts = 3;

        function connectWs() {
          if (disposed) return;
          ws = new WebSocket(wsUrl);
          ws.binaryType = 'arraybuffer';

          ws.onopen = () => {
            if (disposed) return;
            reconnectAttempts = 0;
            // Send token in the first message instead of the URL to avoid leaking it in logs/history
            ws!.send(JSON.stringify({
              type: 'connect',
              token: token || undefined,
              rows: term.rows,
              cols: term.cols,
            }));
          };

          ws.onmessage = (event: MessageEvent) => {
            if (disposed) return;
            if (event.data instanceof ArrayBuffer) {
              term.write(new Uint8Array(event.data));
            } else {
              term.write(event.data);
            }
          };

          ws.onerror = () => {
            if (disposed) return;
            error = 'WebSocket connection failed';
          };

          ws.onclose = () => {
            if (disposed) return;
            if (reconnectAttempts < maxReconnectAttempts) {
              reconnectAttempts++;
              term.write(`\r\n\x1b[33mConnection lost. Reconnecting (${reconnectAttempts}/${maxReconnectAttempts})...\x1b[0m\r\n`);
              setTimeout(connectWs, 2000);
            } else {
              term.write('\r\n\x1b[33mConnection closed.\x1b[0m\r\n');
            }
          };
        }

        connectWs();

        term.onData((data: string) => {
          if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(data);
          }
        });

        const resizeObserver = new ResizeObserver(() => {
          if (disposed) return;
          fitAddon.fit();
          if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({
              type: 'resize',
              rows: term.rows,
              cols: term.cols,
            }));
          }
        });

        resizeObserver.observe(containerEl!);

        return () => {
          disposed = true;
          resizeObserver.disconnect();
          if (ws) {
            ws.close();
          }
          term.dispose();
        };
      } catch (err) {
        if (!disposed) {
          error = err instanceof Error ? err.message : 'Failed to load terminal';
        }
      }
    }

    const cleanupPromise = init();

    return () => {
      disposed = true;
      cleanupPromise.then((cleanup) => cleanup?.());
    };
  });
</script>

{#if error}
  <div class="bg-terminal rounded-lg flex items-center justify-center" style="height: calc(100vh - 280px); min-height: 400px;">
    <p class="text-error text-sm">{error}</p>
  </div>
{:else}
  <div bind:this={containerEl} class="bg-terminal rounded-lg p-2" style="height: calc(100vh - 280px); min-height: 400px;"></div>
{/if}
