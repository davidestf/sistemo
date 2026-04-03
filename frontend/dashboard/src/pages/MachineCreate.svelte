<script lang="ts">
  import type { ImageInfo, NetworkInfo, RegistryImage, BuildStatus, VolumeInfo } from '$lib/api/types';
  import { get, post } from '$lib/api/client';
  import { addToast } from '$lib/stores/toast.svelte';
  import { formatMB } from '$lib/utils/format';
  import Card from '$lib/components/ui/Card.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';

  // --- Tab state ---
  type Tab = 'images' | 'docker' | 'url' | 'dockerfile' | 'github';
  let activeTab = $state<Tab>('images');

  const tabs: Array<{ id: Tab; label: string; disabled?: boolean }> = [
    { id: 'images', label: 'Images' },
    { id: 'docker', label: 'Docker Image' },
    { id: 'url', label: 'URL' },
    { id: 'dockerfile', label: 'Dockerfile', disabled: true },
    { id: 'github', label: 'GitHub', disabled: true },
  ];

  // --- Shared data ---
  let networks = $state<NetworkInfo[]>([]);
  let loading = $state(true);
  let error = $state<string | undefined>();

  // --- Images tab ---
  let registryImages = $state<RegistryImage[]>([]);
  let localImages = $state<ImageInfo[]>([]);
  let downloadingImage = $state<string | null>(null);

  // --- Docker tab ---
  let dockerImage = $state('');
  let buildStatus = $state<BuildStatus | null>(null);
  let building = $state(false);
  let buildPollTimer = $state<ReturnType<typeof setInterval> | null>(null);

  // --- Build logs ---
  let showBuildLogs = $state(false);
  let buildLogs = $state<string[]>([]);
  let buildLogsLoading = $state(false);

  async function fetchBuildLogs() {
    if (!buildStatus?.id) return;
    buildLogsLoading = true;
    try {
      const data = await get<{ lines: string[]; total: number }>(`/api/v1/images/build/${encodeURIComponent(buildStatus.id)}/logs?tail=200`);
      buildLogs = data.lines ?? [];
    } catch {
      buildLogs = ['Failed to load logs'];
    }
    buildLogsLoading = false;
  }

  // --- URL tab ---
  let imageUrl = $state('');

  // --- Config panel ---
  let showConfig = $state(false);
  let selectedImagePath = $state('');
  let selectedImageLabel = $state('');
  let name = $state('');
  let vcpus = $state(1);
  let memoryMb = $state(512);
  let storageMb = $state(2048);
  let selectedNetwork = $state('');
  let rootVolume = $state(''); // boot from existing volume (instead of image)
  let availableVolumes = $state<VolumeInfo[]>([]);
  let selectedVolumes = $state<string[]>([]); // volume IDs to attach
  let deploying = $state(false);

  const vcpuOptions = [1, 2, 4, 8];
  const memoryOptions = [
    { label: '256 MB', value: 256 },
    { label: '512 MB', value: 512 },
    { label: '1 GB', value: 1024 },
    { label: '2 GB', value: 2048 },
    { label: '4 GB', value: 4096 },
    { label: '8 GB', value: 8192 },
    { label: '16 GB', value: 16384 },
  ];
  const storageOptions = [
    { label: '1 GB', value: 1024 },
    { label: '2 GB', value: 2048 },
    { label: '4 GB', value: 4096 },
    { label: '8 GB', value: 8192 },
    { label: '16 GB', value: 16384 },
    { label: '32 GB', value: 32768 },
  ];

  async function fetchData() {
    loading = true;
    error = undefined;
    try {
      const [regData, imgData, netData, volData] = await Promise.all([
        get<{ images: RegistryImage[] }>('/api/v1/registry').catch(() => ({ images: [] as RegistryImage[] })),
        get<{ images: ImageInfo[] }>('/api/v1/images'),
        get<{ networks: NetworkInfo[] }>('/api/v1/networks'),
        get<{ volumes: VolumeInfo[] }>('/api/v1/volumes').catch(() => ({ volumes: [] as VolumeInfo[] })),
      ]);
      registryImages = regData.images ?? [];
      localImages = imgData.images ?? [];
      networks = netData.networks ?? [];
      availableVolumes = (volData.volumes ?? []).filter(v => v.status === 'online' && v.role === 'data');
      if (networks.length > 0) selectedNetwork = networks[0].name;
    } catch (err) {
      error = err instanceof Error ? err.message : 'Failed to load data';
    } finally {
      loading = false;
    }
  }

  // Check for active builds on mount (recovers state after page refresh)
  async function checkActiveBuilds() {
    try {
      const data = await get<{ builds: BuildStatus[] }>('/api/v1/images/builds');
      const active = (data.builds ?? []).find(b => b.status === 'building');
      if (active) {
        // Recover build state
        activeTab = 'docker';
        dockerImage = active.image;
        building = true;
        buildStatus = active;
        startBuildPolling(active.id);
      }
    } catch {
      // Ignore — just means no active builds
    }
  }

  let pollErrors = 0;

  function startBuildPolling(buildKey: string) {
    if (buildPollTimer) clearInterval(buildPollTimer);
    pollErrors = 0;
    buildPollTimer = setInterval(async () => {
      try {
        const status = await get<BuildStatus>(`/api/v1/images/build/${encodeURIComponent(buildKey)}/status`);
        buildStatus = status;
        pollErrors = 0;

        if (status.status === 'complete') {
          if (buildPollTimer) clearInterval(buildPollTimer);
          buildPollTimer = null;
          building = false;
          addToast('Build complete', 'success');
          await fetchData();
          let built = status.image_digest
            ? localImages.find(l => l.digest === status.image_digest)
            : null;
          if (!built) {
            const builtName = status.build_name;
            built = localImages.find(l => l.name === builtName || l.file === builtName + '.rootfs.ext4');
          }
          if (built) {
            selectImage(built.path, built.name);
          }
        } else if (status.status === 'error') {
          if (buildPollTimer) clearInterval(buildPollTimer);
          buildPollTimer = null;
          building = false;
          addToast(`Build failed: ${status.message}`, 'error');
        }
      } catch {
        pollErrors++;
        if (pollErrors > 10) {
          if (buildPollTimer) clearInterval(buildPollTimer);
          buildPollTimer = null;
          building = false;
          addToast('Lost connection to build — check dashboard', 'error');
        }
      }
    }, 3000);
  }

  $effect(() => {
    fetchData().then(() => {
      // Check if navigated from Images page with pre-selected image
      const deployImage = sessionStorage.getItem('sistemo_deploy_image');
      if (deployImage) {
        sessionStorage.removeItem('sistemo_deploy_image');
        try {
          const { path, name: imgName } = JSON.parse(deployImage);
          if (path) selectImage(path, imgName || '');
        } catch { /* ignore bad data */ }
      }

      // Check if navigated from Volumes page with pre-selected root volume
      const deployVol = sessionStorage.getItem('sistemo_deploy_volume');
      if (deployVol) {
        sessionStorage.removeItem('sistemo_deploy_volume');
        rootVolume = deployVol;
        selectedImageLabel = `Volume: ${deployVol}`;
        showConfig = true;
      }
    });
    checkActiveBuilds();
    return () => {
      if (buildPollTimer) clearInterval(buildPollTimer);
      if (downloadPollTimer) clearInterval(downloadPollTimer);
    };
  });

  // --- Select image for config ---
  function selectImage(path: string, label: string) {
    selectedImagePath = path;
    selectedImageLabel = label;
    showConfig = true;
  }

  // --- Download registry image then select ---
  let downloadPollTimer = $state<ReturnType<typeof setInterval> | null>(null);

  async function downloadAndDeploy(img: RegistryImage) {
    downloadingImage = img.name;
    try {
      const result = await post<{ status: string; name: string; path?: string }>('/api/v1/registry/download', { name: img.name });

      if (result.status === 'already_exists') {
        // Already downloaded — just refresh and select
        await fetchData();
        const local = localImages.find(l => l.name === img.name || l.file === img.name + '.rootfs.ext4');
        if (local) selectImage(local.path, local.name);
        downloadingImage = null;
        return;
      }

      if (result.status === 'downloading') {
        // Another request is already downloading this — poll until done
        addToast(`"${img.name}" is already downloading...`, 'info');
        startDownloadPolling(img);
        return;
      }

      // Download completed
      addToast(`Image "${img.name}" downloaded`, 'success');
      await fetchData();
      const local = localImages.find(l => l.name === img.name || l.file === img.name + '.rootfs.ext4');
      if (local) {
        selectImage(local.path, local.name);
      }
      downloadingImage = null;
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Failed to download image', 'error');
      downloadingImage = null;
    }
  }

  function startDownloadPolling(img: RegistryImage) {
    if (downloadPollTimer) clearInterval(downloadPollTimer);
    let pollCount = 0;
    const maxPolls = 200; // 200 * 3s = 10 minutes max
    downloadPollTimer = setInterval(async () => {
      pollCount++;
      if (pollCount > maxPolls) {
        if (downloadPollTimer) { clearInterval(downloadPollTimer); downloadPollTimer = null; }
        downloadingImage = null;
        addToast(`Download of "${img.name}" timed out`, 'error');
        return;
      }
      try {
        const regData = await get<{ images: RegistryImage[] }>('/api/v1/registry');
        const updated = (regData.images ?? []).find(i => i.name === img.name);
        if (updated?.downloaded) {
          if (downloadPollTimer) { clearInterval(downloadPollTimer); downloadPollTimer = null; }
          addToast(`Image "${img.name}" downloaded`, 'success');
          await fetchData();
          const local = localImages.find(l => l.name === img.name || l.file === img.name + '.rootfs.ext4');
          if (local) selectImage(local.path, local.name);
          downloadingImage = null;
        }
      } catch {
        // Keep polling on transient errors
      }
    }, 3000);
  }

  // --- Docker build ---
  async function handleDockerBuild() {
    if (!dockerImage.trim()) {
      addToast('Enter a Docker image name', 'error');
      return;
    }

    building = true;
    buildStatus = null;
    showConfig = false;

    try {
      const buildResp = await post<{ image: string; build_name: string; id: string }>('/api/v1/images/build', { image: dockerImage.trim() });
      addToast('Build started...', 'info');
      startBuildPolling(buildResp.id);
    } catch (err) {
      building = false;
      addToast(err instanceof Error ? err.message : 'Failed to start build', 'error');
    }
  }

  // --- URL select ---
  function selectUrl() {
    if (!imageUrl.trim()) {
      addToast('Enter a URL to a rootfs image', 'error');
      return;
    }
    selectImage(imageUrl.trim(), imageUrl.trim().split('/').pop() ?? 'URL image');
  }

  // --- Deploy ---
  async function handleDeploy(e: SubmitEvent) {
    e.preventDefault();
    if (!selectedImagePath && !rootVolume) {
      addToast('Please select an image or volume', 'error');
      return;
    }

    deploying = true;
    try {
      const body: Record<string, unknown> = {
        vcpus,
        memory_mb: memoryMb,
        storage_mb: storageMb,
      };
      if (rootVolume) {
        body.root_volume = rootVolume;
      } else {
        body.image = selectedImagePath;
      }
      if (name.trim()) body.name = name.trim();
      if (selectedNetwork && selectedNetwork !== 'default') {
        body.network_name = selectedNetwork;
      }
      if (selectedVolumes.length > 0) {
        body.attached_storage = selectedVolumes;
      }

      const result = await post<{ machine_id: string }>('/api/v1/machines', body);
      addToast('Machine deployed successfully', 'success');
      window.location.hash = '#/machines';
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Failed to deploy machine';
      // Browser "NetworkError" usually means the request was interrupted (timeout, connection reset).
      // The machine may have been created — check the machines list.
      if (msg.includes('NetworkError') || msg.includes('fetch')) {
        addToast('Connection interrupted during deploy — the machine may still be starting. Check the machines list.', 'warning');
        window.location.hash = '#/machines';
      } else {
        addToast(msg, 'error');
      }
    } finally {
      deploying = false;
    }
  }

  function resetConfig() {
    showConfig = false;
    selectedImagePath = '';
    selectedImageLabel = '';
    name = '';
    vcpus = 1;
    memoryMb = 512;
    storageMb = 2048;
    selectedVolumes = [];
  }
</script>

{#if loading}
  <div class="flex items-center justify-center py-20">
    <Spinner />
  </div>
{:else if error}
  <div class="flex flex-col items-center gap-3 py-20">
    <p class="text-error text-sm">{error}</p>
    <button onclick={fetchData} class="text-accent text-sm hover:underline cursor-pointer bg-transparent border-none">Retry</button>
  </div>
{:else}
  <div class="max-w-4xl mx-auto space-y-6">
    <h2 class="text-xl font-semibold text-text">Deploy Machine</h2>

    <!-- Tab Navigation -->
    <div class="flex border-b border-border">
      {#each tabs as tab}
        <button
          onclick={() => { if (!tab.disabled) { activeTab = tab.id; resetConfig(); } }}
          disabled={tab.disabled}
          class="px-4 py-2.5 text-sm font-medium transition border-b-2 -mb-px cursor-pointer bg-transparent
            {activeTab === tab.id
              ? 'border-accent text-accent'
              : tab.disabled
                ? 'border-transparent text-muted/40 cursor-not-allowed'
                : 'border-transparent text-muted hover:text-text hover:border-border'}"
        >
          {tab.label}
          {#if tab.disabled}
            <span class="ml-1 text-xs text-muted/40">(soon)</span>
          {/if}
        </button>
      {/each}
    </div>

    <!-- Tab Content -->
    {#if activeTab === 'images'}
      <!-- Registry Images -->
      {#if registryImages.length > 0}
        <div>
          <h3 class="text-sm font-medium text-muted uppercase tracking-wider mb-3">Registry Images</h3>
          <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
            {#each registryImages as img (img.name)}
              <Card>
                <div class="flex items-start justify-between">
                  <div>
                    <p class="font-medium text-text text-sm">{img.name}</p>
                    {#if img.description}
                      <p class="text-xs text-muted mt-1">{img.description}</p>
                    {/if}
                    <p class="text-xs text-muted mt-1">{img.arch}</p>
                  </div>
                  <div class="flex-shrink-0 ml-3">
                    {#if img.downloaded}
                      {@const local = localImages.find(l => l.name === img.name || l.file === img.name + '.rootfs.ext4')}
                      <Button
                        variant="primary"
                        size="sm"
                        onclick={() => {
                          if (local) selectImage(local.path, local.name);
                        }}
                      >
                        Deploy
                      </Button>
                    {:else}
                      <Button
                        variant="secondary"
                        size="sm"
                        loading={downloadingImage === img.name}
                        onclick={() => downloadAndDeploy(img)}
                      >
                        {downloadingImage === img.name ? 'Downloading...' : 'Download & Deploy'}
                      </Button>
                    {/if}
                  </div>
                </div>
              </Card>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Tip: local images are managed on the Images page -->
      <p class="text-xs text-muted mt-4">Already have local images? Go to <a href="#/images" class="text-accent hover:underline">Images</a> to deploy from them.</p>

    {:else if activeTab === 'docker'}
      <Card>
        <h3 class="text-sm font-medium text-text mb-4">Build from Docker Image</h3>
        <div class="flex items-end gap-3">
          <div class="flex-1">
            <label for="docker-image" class="block text-xs text-muted mb-1">Docker image</label>
            <input
              id="docker-image"
              type="text"
              bind:value={dockerImage}
              placeholder="nginx:latest"
              disabled={building}
              class="w-full px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-text placeholder:text-muted/50 focus:outline-none focus:border-accent disabled:opacity-50"
            />
          </div>
          <Button variant="primary" loading={building} onclick={handleDockerBuild}>
            {building ? 'Building...' : 'Build & Deploy'}
          </Button>
        </div>

        {#if buildStatus}
          <div class="mt-4 p-3 rounded-lg border {buildStatus.status === 'building'
              ? 'bg-accent/5 border-accent/20'
              : buildStatus.status === 'complete'
                ? 'bg-success/5 border-success/20'
                : 'bg-error/5 border-error/20'}">
            <div class="flex items-center gap-2 mb-2">
              {#if buildStatus.status === 'building'}
                <Spinner size="sm" />
                <span class="text-sm font-medium text-accent">{buildStatus.progress_msg || 'Building image...'}</span>
                <button
                  onclick={async () => {
                    try {
                      await post(`/api/v1/images/build/${encodeURIComponent(buildStatus?.id ?? '')}/cancel`);
                      if (buildPollTimer) { clearInterval(buildPollTimer); buildPollTimer = null; }
                      building = false;
                      buildStatus = { ...buildStatus!, status: 'error', message: 'Cancelled by user' };
                      addToast('Build cancelled', 'info');
                    } catch (e) {
                      addToast('Failed to cancel build', 'error');
                    }
                  }}
                  class="ml-auto text-xs text-muted hover:text-error cursor-pointer bg-transparent border-none transition"
                >Cancel</button>
              {:else if buildStatus.status === 'complete'}
                <svg class="w-4 h-4 text-success" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7" />
                </svg>
                <span class="text-sm font-medium text-success">Build complete</span>
              {:else}
                <svg class="w-4 h-4 text-error" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
                </svg>
                <span class="text-sm font-medium text-error">Build failed</span>
              {/if}
            </div>
            {#if buildStatus.status === 'building' && (buildStatus.progress ?? 0) > 0}
              <div class="w-full bg-surface-2 rounded-full h-1.5 mt-2">
                <div class="bg-accent h-1.5 rounded-full transition-all duration-500" style="width: {buildStatus.progress}%"></div>
              </div>
            {/if}
            {#if buildStatus.status === 'error' && buildStatus.message}
              <pre class="text-xs text-muted bg-terminal rounded p-2 mt-2 overflow-x-auto max-h-32 whitespace-pre-wrap">{buildStatus.message}</pre>
            {/if}
            <div class="mt-2">
              <button
                onclick={() => { showBuildLogs = !showBuildLogs; if (showBuildLogs) fetchBuildLogs(); }}
                class="text-xs text-muted hover:text-accent cursor-pointer bg-transparent border-none transition flex items-center gap-1"
              >
                <svg class="w-3 h-3 transition-transform {showBuildLogs ? 'rotate-90' : ''}" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7" />
                </svg>
                {showBuildLogs ? 'Hide' : 'Show'} build logs
                {#if buildStatus.status === 'building'}
                  <button
                    onclick={(e) => { e.stopPropagation(); fetchBuildLogs(); }}
                    class="ml-1 text-muted hover:text-accent cursor-pointer bg-transparent border-none"
                    title="Refresh logs"
                  >
                    <svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                    </svg>
                  </button>
                {/if}
              </button>
              {#if showBuildLogs}
                <div class="mt-1 bg-terminal rounded-lg border border-border overflow-hidden">
                  {#if buildLogsLoading && buildLogs.length === 0}
                    <div class="p-3 text-xs text-muted flex items-center gap-2">
                      <Spinner size="sm" /> Loading logs...
                    </div>
                  {:else if buildLogs.length === 0}
                    <div class="p-3 text-xs text-muted">No logs available yet</div>
                  {:else}
                    <pre class="text-xs text-muted p-3 overflow-x-auto max-h-64 overflow-y-auto whitespace-pre font-mono leading-relaxed">{buildLogs.join('\n')}</pre>
                  {/if}
                </div>
              {/if}
            </div>
          </div>
        {/if}
      </Card>

    {:else if activeTab === 'url'}
      <Card>
        <h3 class="text-sm font-medium text-text mb-4">Deploy from URL</h3>
        <p class="text-xs text-muted mb-4">Provide a direct URL to a rootfs.ext4 image file.</p>
        <div class="flex items-end gap-3">
          <div class="flex-1">
            <label for="image-url" class="block text-xs text-muted mb-1">Image URL</label>
            <input
              id="image-url"
              type="url"
              bind:value={imageUrl}
              placeholder="https://example.com/image.rootfs.ext4"
              class="w-full px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-text placeholder:text-muted/50 focus:outline-none focus:border-accent"
            />
          </div>
          <Button variant="primary" onclick={selectUrl}>Configure</Button>
        </div>
      </Card>

    {:else if activeTab === 'dockerfile'}
      <Card>
        <div class="text-center py-8">
          <div class="inline-flex items-center justify-center w-12 h-12 rounded-full bg-surface-hover mb-4">
            <svg class="w-6 h-6 text-muted" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M19 11H5m14 0a2 2 0 012 2v6a2 2 0 01-2 2H5a2 2 0 01-2-2v-6a2 2 0 012-2m14 0V9a2 2 0 00-2-2M5 11V9a2 2 0 012-2m0 0V5a2 2 0 012-2h6a2 2 0 012 2v2M7 7h10" />
            </svg>
          </div>
          <h3 class="text-sm font-medium text-text mb-2">Coming Soon</h3>
          <p class="text-sm text-muted mb-1">Build machine images from Dockerfiles</p>
          <p class="text-xs text-muted">For now: <code class="bg-surface-inner px-1.5 py-0.5 rounded text-accent">docker build -t myapp .</code> then use the Docker Image tab</p>
        </div>
      </Card>

    {:else if activeTab === 'github'}
      <Card>
        <div class="text-center py-8">
          <div class="inline-flex items-center justify-center w-12 h-12 rounded-full bg-surface-hover mb-4">
            <svg class="w-6 h-6 text-muted" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4" />
            </svg>
          </div>
          <h3 class="text-sm font-medium text-text mb-2">Coming Soon</h3>
          <p class="text-sm text-muted">Connect GitHub repos for auto-build & deploy</p>
        </div>
      </Card>
    {/if}

    <!-- Config Panel -->
    {#if showConfig}
      <Card>
        <div class="flex items-center justify-between mb-5">
          <div>
            <h3 class="text-sm font-medium text-text">Configure & Deploy</h3>
            <p class="text-xs text-muted mt-1">Image: {selectedImageLabel}</p>
          </div>
          <button
            onclick={resetConfig}
            class="text-xs text-muted hover:text-text transition cursor-pointer bg-transparent border-none"
          >
            Change image
          </button>
        </div>

        <form onsubmit={handleDeploy} class="space-y-5">
          <!-- Name -->
          <div>
            <label for="machine-name" class="block text-xs text-muted mb-1">Name</label>
            <input
              id="machine-name"
              type="text"
              bind:value={name}
              placeholder="auto-generated"
              class="w-full px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-text placeholder:text-muted/50 focus:outline-none focus:border-accent"
            />
          </div>

          <!-- vCPUs -->
          <div>
            <label class="block text-xs text-muted mb-1">vCPUs</label>
            <div class="flex gap-2">
              {#each vcpuOptions as opt}
                <button
                  type="button"
                  onclick={() => { vcpus = opt; }}
                  class="px-4 py-2 rounded-lg text-sm font-medium transition cursor-pointer border
                    {vcpus === opt
                      ? 'bg-accent/15 text-accent border-accent/30'
                      : 'bg-surface-inner text-muted border-border hover:text-text'}"
                >
                  {opt}
                </button>
              {/each}
            </div>
          </div>

          <!-- Memory -->
          <div>
            <label class="block text-xs text-muted mb-1">Memory</label>
            <div class="flex flex-wrap gap-2">
              {#each memoryOptions as opt}
                <button
                  type="button"
                  onclick={() => { memoryMb = opt.value; }}
                  class="px-4 py-2 rounded-lg text-sm font-medium transition cursor-pointer border
                    {memoryMb === opt.value
                      ? 'bg-accent/15 text-accent border-accent/30'
                      : 'bg-surface-inner text-muted border-border hover:text-text'}"
                >
                  {opt.label}
                </button>
              {/each}
            </div>
          </div>

          <!-- Storage -->
          <div>
            <label class="block text-xs text-muted mb-1">Storage</label>
            <div class="flex flex-wrap gap-2">
              {#each storageOptions as opt}
                <button
                  type="button"
                  onclick={() => { storageMb = opt.value; }}
                  class="px-4 py-2 rounded-lg text-sm font-medium transition cursor-pointer border
                    {storageMb === opt.value
                      ? 'bg-accent/15 text-accent border-accent/30'
                      : 'bg-surface-inner text-muted border-border hover:text-text'}"
                >
                  {opt.label}
                </button>
              {/each}
            </div>
          </div>

          <!-- Network -->
          <div>
            <label for="network" class="block text-xs text-muted mb-1">Network</label>
            <select
              id="network"
              bind:value={selectedNetwork}
              class="w-full px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-text focus:outline-none focus:border-accent"
            >
              {#each networks as net}
                <option value={net.name}>{net.name} ({net.subnet})</option>
              {/each}
            </select>
          </div>

          <!-- Attach Volumes -->
          {#if availableVolumes.length > 0}
            <div>
              <label for="attach-volume" class="block text-xs text-muted mb-1">Attach Data Volumes</label>
              <div class="flex gap-2">
                <select
                  id="attach-volume"
                  class="flex-1 px-3 py-2 bg-surface-inner border border-border rounded-lg text-sm text-text focus:outline-none focus:border-accent"
                >
                  <option value="">Select a volume...</option>
                  {#each availableVolumes.filter(v => !selectedVolumes.includes(v.id)) as vol}
                    <option value={vol.id}>{vol.name} ({formatMB(vol.size_mb)})</option>
                  {/each}
                </select>
                <Button variant="secondary" onclick={() => {
                  const sel = document.getElementById('attach-volume') as HTMLSelectElement;
                  if (sel?.value) {
                    selectedVolumes = [...selectedVolumes, sel.value];
                    sel.value = '';
                  }
                }}>Add</Button>
              </div>
              {#if selectedVolumes.length > 0}
                <div class="mt-2 space-y-1">
                  {#each selectedVolumes as volId}
                    {@const vol = availableVolumes.find(v => v.id === volId)}
                    {#if vol}
                      <div class="flex items-center justify-between px-3 py-1.5 bg-surface-inner border border-border rounded-lg text-sm">
                        <span class="text-text">{vol.name}</span>
                        <div class="flex items-center gap-2">
                          <span class="text-muted text-xs">{formatMB(vol.size_mb)}</span>
                          <button
                            type="button"
                            onclick={() => { selectedVolumes = selectedVolumes.filter(v => v !== volId); }}
                            class="text-muted hover:text-error text-xs cursor-pointer bg-transparent border-none transition"
                          >Remove</button>
                        </div>
                      </div>
                    {/if}
                  {/each}
                </div>
              {/if}
            </div>
          {/if}

          <!-- Submit -->
          <Button variant="primary" loading={deploying}>
            {deploying ? 'Deploying...' : 'Deploy'}
          </Button>
        </form>
      </Card>
    {/if}
  </div>

  {#if deploying}
    <div class="fixed inset-0 bg-black/40 z-50 flex items-center justify-center">
      <div class="bg-surface rounded-xl border border-border p-8 flex flex-col items-center gap-4 max-w-sm">
        <Spinner size="md" />
        <p class="text-text font-medium">Deploying Machine...</p>
        <p class="text-xs text-muted">This usually takes 3-7 seconds</p>
      </div>
    </div>
  {/if}
{/if}
