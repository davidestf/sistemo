export interface PortRule {
  host_port: number;
  machine_port: number;
  protocol: string;
}

export interface Machine {
  id: string;
  name: string;
  status: string;
  maintenance_operation?: string;
  image: string;
  ip_address: string;
  namespace?: string;
  vcpus: number;
  memory_mb: number;
  storage_mb: number;
  network_name: string;
  created_at: string;
  last_state_change: string;
  port_rules: PortRule[];
  pid: number;
  image_digest?: string;
}

export interface AuditEntry {
  id: number;
  timestamp: string;
  action: string;
  target_type: string;
  target_id: string;
  target_name: string;
  details: string;
  success: boolean;
}

export interface ImageInfo {
  name: string;
  file: string;
  path: string;
  size_mb: number;
  created_at: string;
  source: string;
  digest?: string;
  verified?: boolean;
}

export interface VolumeInfo {
  id: string;
  name: string;
  size_mb: number;
  path: string;
  status: 'online' | 'attached' | 'maintenance' | 'error';
  machine_id: string | null;
  role: 'data' | 'root';
  created: string;
  last_state_change: string;
}

export interface RegistryImage {
  name: string;
  description: string;
  file: string;
  arch: string;
  downloaded: boolean;
}

export interface BuildStatus {
  id: string;
  status: 'building' | 'complete' | 'error';
  image: string;
  build_name: string;
  message: string;
  started_at: string;
  image_digest?: string;
  progress?: number;      // 0-100 build progress percentage
  progress_msg?: string;  // current build step description
}

export interface NetworkInfo {
  name: string;
  subnet: string;
  bridge_name: string;
  machine_count: number;
  created_at?: string;
}

export interface SystemInfo {
  health: {
    status: string;
    checks: {
      firecracker: boolean;
      kernel: boolean;
    };
  };
  host?: {
    hostname: string;
    kernel: string;
    cpus: number;
    memory_mb: number;
    disk_total_gb: number;
    disk_used_gb: number;
    disk_free_gb: number;
  };
  daemon: {
    go_version: string;
    arch: string;
    bridge: string;
    goroutines: number;
  };
  stats: {
    total: number;
    running: number;
    stopped: number;
    errored: number;
    vcpus_allocated: number;
    memory_mb_allocated: number;
  };
  limits: {
    max_vcpus: number;
    max_memory_mb: number;
    max_storage_mb: number;
  };
}
