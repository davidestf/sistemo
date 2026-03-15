#!/bin/sh
# Sistemo VM init — injected into rootfs by sistemo build.
export PATH=/usr/sbin:/sbin:/usr/bin:/bin
set -e
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mkdir -p /var/run /var/log /tmp /run/systemd
printf 'nameserver 8.8.8.8\nnameserver 1.1.1.1\n' > /etc/resolv.conf

# Wait for virtio-net device then configure 10.0.0.2/24.
sleep 2
IP=/usr/sbin/ip
[ -x "$IP" ] || IP=/sbin/ip
i=0; while [ $i -lt 25 ]; do
  [ -d /sys/class/net/eth0 ] && break
  sleep 1; i=$((i+1))
done
if [ -d /sys/class/net/eth0 ]; then
  $IP link set eth0 up 2>/dev/null || true
  $IP addr add 10.0.0.2/24 dev eth0 2>/dev/null || true
  $IP route add default via 10.0.0.1 2>/dev/null || true
else
  for x in /sys/class/net/*; do
    [ -d "$x" ] || continue
    iface=$(basename "$x"); [ "$iface" = "lo" ] && continue
    $IP link set "$iface" up 2>/dev/null || true
    $IP addr add 10.0.0.2/24 dev "$iface" 2>/dev/null || true
    $IP route add default via 10.0.0.1 2>/dev/null || true
    break
  done
fi
exec /lib/systemd/systemd --log-target=console --log-level=warning 2>/dev/null || exec /usr/lib/systemd/systemd --log-target=console --log-level=warning
