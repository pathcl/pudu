#!/bin/bash
# Build a minimal Ubuntu 24.04 (noble) ext4 rootfs using debootstrap.
set -e

ROOTFS="${1:-rootfs.ext4}"
SIZE="${2:-2G}"
MOUNTPOINT="/tmp/rootfs-build-$$"

echo "==> Building Ubuntu 24.04 rootfs: $ROOTFS ($SIZE)"

# Create empty ext4 image
truncate -s "$SIZE" "$ROOTFS"
mkfs.ext4 -F -L rootfs "$ROOTFS" > /dev/null

mkdir -p "$MOUNTPOINT"
sudo mount -o loop "$ROOTFS" "$MOUNTPOINT"
trap "sudo umount -l $MOUNTPOINT/dev/pts 2>/dev/null || true; \
      sudo umount -l $MOUNTPOINT/proc  2>/dev/null || true; \
      sudo umount -l $MOUNTPOINT/sys   2>/dev/null || true; \
      sudo umount -l $MOUNTPOINT/dev   2>/dev/null || true; \
      sudo umount -l $MOUNTPOINT       2>/dev/null || true; \
      sudo rmdir $MOUNTPOINT           2>/dev/null || true" EXIT

# Debootstrap stage 1+2
echo "==> Running mmdebstrap (noble, minbase)..."
# Install mmdebstrap if missing
if ! command -v mmdebstrap &>/dev/null; then
  sudo apt-get install -y mmdebstrap
fi
sudo mmdebstrap \
  --variant=minbase \
  --arch=amd64 \
  --include=openssh-server,systemd,systemd-sysv,udev,iproute2,iputils-ping,curl,ca-certificates,passwd \
  noble "$MOUNTPOINT" https://archive.ubuntu.com/ubuntu

# Bind mounts for chroot
sudo mount --bind /proc    "$MOUNTPOINT/proc"
sudo mount --bind /sys     "$MOUNTPOINT/sys"
sudo mount --bind /dev     "$MOUNTPOINT/dev"
sudo mount --bind /dev/pts "$MOUNTPOINT/dev/pts"
sudo cp /etc/resolv.conf "$MOUNTPOINT/etc/resolv.conf"

# Add universe repo and install packages
echo "==> Installing packages..."
sudo chroot "$MOUNTPOINT" bash -c "
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    openssh-server \
    systemd \
    systemd-sysv \
    udev \
    iproute2 \
    iputils-ping \
    curl \
    ca-certificates \
    passwd
"

# Root password
echo "==> Setting root password..."
echo "root:root" | sudo chroot "$MOUNTPOINT" chpasswd

# Generate SSH host keys (mmdebstrap --variant=minbase skips postinstall scripts)
echo "==> Generating SSH host keys..."
sudo chroot "$MOUNTPOINT" ssh-keygen -A

# SSH config — use a drop-in that wins over any other config
echo "==> Configuring SSH..."
sudo mkdir -p "$MOUNTPOINT/etc/ssh/sshd_config.d"
cat <<'EOF' | sudo tee "$MOUNTPOINT/etc/ssh/sshd_config.d/99-pudu.conf" > /dev/null
PermitRootLogin yes
PasswordAuthentication yes
UseDNS no
EOF

# Enable SSH service (not just socket)
sudo chroot "$MOUNTPOINT" systemctl enable ssh 2>/dev/null || true
sudo chroot "$MOUNTPOINT" systemctl disable ssh.socket 2>/dev/null || true

# Mask networkd so the kernel-assigned IP (from ip= kernel arg) is not reset
sudo chroot "$MOUNTPOINT" systemctl mask systemd-networkd 2>/dev/null || true
sudo chroot "$MOUNTPOINT" systemctl mask systemd-networkd-wait-online 2>/dev/null || true
sudo chroot "$MOUNTPOINT" systemctl mask networkd-dispatcher 2>/dev/null || true

# Disable apparmor — not needed in a microVM, can block sshd
sudo chroot "$MOUNTPOINT" systemctl mask apparmor 2>/dev/null || true

# Static DNS (systemd-resolved not running in VM)
sudo rm -f "$MOUNTPOINT/etc/resolv.conf"
echo 'nameserver 8.8.8.8' | sudo tee "$MOUNTPOINT/etc/resolv.conf" > /dev/null

# Clean apt cache to shrink image
sudo chroot "$MOUNTPOINT" apt-get clean

echo "==> Rootfs ready ($(du -h "$ROOTFS" | cut -f1))"
