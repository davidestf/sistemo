<script lang="ts">
  import Shell from './components/layout/Shell.svelte';
  import Toast from './lib/components/ui/Toast.svelte';
  import Spinner from './lib/components/ui/Spinner.svelte';
  import ErrorBoundary from './lib/components/ui/ErrorBoundary.svelte';
  import Login from './pages/Login.svelte';
  import Setup from './pages/Setup.svelte';
  import Dashboard from './pages/Dashboard.svelte';
  import VMList from './pages/VMList.svelte';
  import VMCreate from './pages/VMCreate.svelte';
  import VMDetail from './pages/VMDetail.svelte';
  import Networks from './pages/Networks.svelte';
  import Volumes from './pages/Volumes.svelte';
  import Images from './pages/Images.svelte';
  import History from './pages/History.svelte';
  import System from './pages/System.svelte';
  import { getAuthState, checkAuth } from './lib/stores/auth.svelte';

  // Check auth on mount
  $effect(() => {
    checkAuth();
  });

  let authState = $derived(getAuthState());

  let hash = $state(window.location.hash || '#/');

  $effect(() => {
    const onHashChange = () => { hash = window.location.hash || '#/'; };
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  });

  type Route =
    | { page: 'dashboard' }
    | { page: 'vms' }
    | { page: 'vm-create' }
    | { page: 'vm-detail'; id: string }
    | { page: 'networks' }
    | { page: 'volumes' }
    | { page: 'images' }
    | { page: 'history' }
    | { page: 'system' };

  let route: Route = $derived.by(() => {
    // Strip query params from hash for route matching
    const raw = hash.replace(/^#/, '') || '/';
    const h = raw.split('?')[0];

    if (h === '/') return { page: 'dashboard' };
    if (h === '/vms') return { page: 'vms' };
    if (h === '/vms/create') return { page: 'vm-create' };
    if (h === '/networks') return { page: 'networks' };
    if (h === '/volumes') return { page: 'volumes' };
    if (h === '/images') return { page: 'images' };
    if (h === '/history') return { page: 'history' };
    if (h === '/system') return { page: 'system' };

    const vmMatch = h.match(/^\/vms\/([^/]+)$/);
    if (vmMatch) return { page: 'vm-detail', id: vmMatch[1] };

    return { page: 'dashboard' };
  });

  let title = $derived.by(() => {
    switch (route.page) {
      case 'dashboard': return 'Dashboard';
      case 'vms': return 'Virtual Machines';
      case 'vm-create': return 'Deploy Virtual Machine';
      case 'vm-detail': return 'VM Details';
      case 'networks': return 'Networks';
      case 'volumes': return 'Volumes';
      case 'images': return 'Images';
      case 'history': return 'History';
      case 'system': return 'System';
      default: return 'Dashboard';
    }
  });
</script>

{#if authState === 'loading'}
  <div class="min-h-screen bg-base flex items-center justify-center">
    <Spinner size="md" />
  </div>
{:else if authState === 'setup'}
  <Setup />
{:else if authState === 'login'}
  <Login />
{:else}
  <Shell {title}>
    <ErrorBoundary>
      {#if route.page === 'dashboard'}
        <Dashboard />
      {:else if route.page === 'vms'}
        <VMList />
      {:else if route.page === 'vm-create'}
        <VMCreate />
      {:else if route.page === 'vm-detail'}
        <VMDetail vmId={route.id} />
      {:else if route.page === 'networks'}
        <Networks />
      {:else if route.page === 'volumes'}
        <Volumes />
      {:else if route.page === 'images'}
        <Images />
      {:else if route.page === 'history'}
        <History />
      {:else if route.page === 'system'}
        <System />
      {/if}
    </ErrorBoundary>
  </Shell>
{/if}

<Toast />
