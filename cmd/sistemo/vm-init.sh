#!/bin/sh
# Sistemo VM init — injected into rootfs by sistemo build.
# The kernel ip= boot parameter configures eth0 with the VM's unique bridge IP
# before this script runs. We just need to set up mounts, DNS, and start systemd.
export PATH=/usr/sbin:/sbin:/usr/bin:/bin
set -e
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mkdir -p /var/run /var/log /tmp /run/systemd
printf 'nameserver 8.8.8.8\nnameserver 1.1.1.1\n' > /etc/resolv.conf

# Remove policy-rc.d so services auto-start on apt-get install
rm -f /usr/sbin/policy-rc.d

# Wait for virtio-net device (kernel ip= already configured it, just ensure it's up)
IP=/usr/sbin/ip
[ -x "$IP" ] || IP=/sbin/ip
i=0; while [ $i -lt 25 ]; do
  [ -d /sys/class/net/eth0 ] && break
  sleep 1; i=$((i+1))
done
if [ -d /sys/class/net/eth0 ]; then
  $IP link set eth0 up 2>/dev/null || true
fi
exec /lib/systemd/systemd --log-target=console --log-level=warning 2>/dev/null || exec /usr/lib/systemd/systemd --log-target=console --log-level=warning
