#!/bin/bash
set -e

ROOTFS="${1:-rootfs.ext4}"
AGENT_BIN="${2:-onfire-agent}"
MOUNTPOINT="/tmp/rootfs-setup-$$"

if [ ! -f "$ROOTFS" ]; then
    echo "Error: rootfs not found: $ROOTFS" >&2
    exit 1
fi

echo "==> Setting up rootfs: $ROOTFS"

# Expand base image to 1GB so there's room to install packages
echo "==> Expanding rootfs to 1GB..."
truncate -s 1G "$ROOTFS"
e2fsck -f -y "$ROOTFS" > /dev/null 2>&1 || true
resize2fs "$ROOTFS" > /dev/null 2>&1

mkdir -p "$MOUNTPOINT"

sudo mount -o loop "$ROOTFS" "$MOUNTPOINT"
trap "sudo umount -l $MOUNTPOINT/dev/pts 2>/dev/null; sudo umount -l $MOUNTPOINT/proc 2>/dev/null; sudo umount -l $MOUNTPOINT/sys 2>/dev/null; sudo umount -l $MOUNTPOINT/dev 2>/dev/null; sudo umount -l $MOUNTPOINT 2>/dev/null; sudo rmdir $MOUNTPOINT 2>/dev/null" EXIT

# Bind mount host proc/sys/dev and resolv.conf so chroot has networking and /proc
sudo mount --bind /proc "$MOUNTPOINT/proc"
sudo mount --bind /sys "$MOUNTPOINT/sys"
sudo mount --bind /dev "$MOUNTPOINT/dev"
sudo mount --bind /dev/pts "$MOUNTPOINT/dev/pts"
sudo cp /etc/resolv.conf "$MOUNTPOINT/etc/resolv.conf"

# Ensure dpkg database exists (Firecracker CI image is very minimal)
echo "==> Initialising dpkg database..."
sudo mkdir -p "$MOUNTPOINT/var/lib/dpkg/updates" \
             "$MOUNTPOINT/var/lib/dpkg/info" \
             "$MOUNTPOINT/var/lib/apt/lists/partial" \
             "$MOUNTPOINT/var/cache/apt/archives/partial"
sudo touch "$MOUNTPOINT/var/lib/dpkg/status" \
           "$MOUNTPOINT/var/lib/dpkg/available"
sudo chroot "$MOUNTPOINT" dpkg --configure -a 2>/dev/null || true
sudo chroot "$MOUNTPOINT" apt-get update -qq 2>/dev/null && echo "  ✓ apt ready" || echo "  (apt-get update failed — package install will happen at runtime)"

# Set root password
echo "==> Setting root password to 'root'..."
echo "root:root" | sudo chroot "$MOUNTPOINT" chpasswd

# Use a drop-in so it wins over any default that disables password auth
echo "==> Configuring SSH for password login..."
sudo mkdir -p "$MOUNTPOINT/etc/ssh/sshd_config.d"
cat <<'EOF' | sudo tee "$MOUNTPOINT/etc/ssh/sshd_config.d/99-onfire.conf" > /dev/null
PermitRootLogin yes
PasswordAuthentication yes
UseDNS no
EOF

# Drop AcceptEnv so the SSH client's locale is not forwarded into the VM.
# The Firecracker CI image has no compiled locales; forwarding LC_ALL causes
# a bash warning on every login.
sudo sed -i '/^AcceptEnv/d' "$MOUNTPOINT/etc/ssh/sshd_config"

# Enable SSH service
echo "==> Enabling SSH service..."
sudo chroot "$MOUNTPOINT" systemctl enable ssh > /dev/null 2>&1 || true

# Install onfire-agent
if [ -f "$AGENT_BIN" ]; then
    echo "==> Installing onfire-agent..."
    sudo cp "$AGENT_BIN" "$MOUNTPOINT/usr/local/bin/onfire-agent"
    sudo chmod +x "$MOUNTPOINT/usr/local/bin/onfire-agent"

    # Install systemd service unit
    cat <<'UNIT' | sudo tee "$MOUNTPOINT/etc/systemd/system/onfire-agent.service" > /dev/null
[Unit]
Description=Onfire Fault Injection Agent
After=network.target

[Service]
ExecStart=/usr/local/bin/onfire-agent
Restart=always
RestartSec=2
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
UNIT

    sudo chroot "$MOUNTPOINT" systemctl enable onfire-agent > /dev/null 2>&1 || true
    echo "  ✓ onfire-agent installed and enabled"
else
    echo "  (skipping onfire-agent: $AGENT_BIN not found)"
fi

# Install onfire CLI helper for use inside the VM (hint requests, status)
echo "==> Installing onfire CLI helper..."
cat <<'ONFIRE_SCRIPT' | sudo tee "$MOUNTPOINT/usr/local/bin/onfire" > /dev/null
#!/bin/sh
# Derive this VM's ID from the default gateway: 172.16.N.1 → N
GATEWAY=$(ip route show default | awk '{print $3; exit}')
VMID=$(echo "$GATEWAY" | cut -d. -f3)
SERVER="http://${GATEWAY}:8888"

_post() {
    if command -v curl >/dev/null 2>&1; then
        curl -sf -X POST "$1"
    else
        wget -qO- --post-data='' "$1"
    fi
}

case "${1:-}" in
    hint)
        result=$(_post "${SERVER}/api/v1/vms/${VMID}/hint") || {
            echo "error: could not reach onfire server at ${SERVER}" >&2
            exit 1
        }
        hint=$(echo "$result" | grep -o '"hint":"[^"]*"' | cut -d'"' -f4)
        score=$(echo "$result" | grep -o '"score":[0-9]*' | cut -d: -f2)
        echo "Hint: ${hint}"
        echo "Current score: ${score}"
        ;;
    *)
        echo "Usage: onfire hint"
        ;;
esac
ONFIRE_SCRIPT
sudo chmod +x "$MOUNTPOINT/usr/local/bin/onfire"
echo "  ✓ onfire helper installed"

# Install stress-ng for realistic CPU fault injection (best-effort)
echo "==> Installing stress-ng (optional, improves CPU fault accuracy)..."
sudo chroot "$MOUNTPOINT" apt-get install -y --no-install-recommends stress-ng > /dev/null 2>&1 && \
    echo "  ✓ stress-ng installed" || \
    echo "  (stress-ng install failed — CPU faults will use pure-Go fallback)"

echo "✓ Rootfs setup complete!"
