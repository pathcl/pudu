#!/bin/bash
set -e

OUTPUT="${1:-cloud-init.iso}"
CONFIG="${2:-cloud-init-config.yaml}"
HOSTNAME="${3:-}"

if [ ! -f "$CONFIG" ]; then
    echo "Error: cloud-init config file not found: $CONFIG" >&2
    exit 1
fi

# Create temporary directory structure
TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

mkdir -p "$TMPDIR/nocloud"

# Generate instance-id (use hostname if provided)
if [ -n "$HOSTNAME" ]; then
    INSTANCE_ID="iid-$HOSTNAME"
else
    INSTANCE_ID="iid-firecracker-vm"
fi

cat > "$TMPDIR/nocloud/meta-data" << EOF
instance-id: $INSTANCE_ID
hostname: ${HOSTNAME:-firecracker-vm}
EOF

# If hostname is provided, inject it into the config
if [ -n "$HOSTNAME" ]; then
    USERDATA=$(mktemp)
    trap "rm -f $USERDATA" EXIT
    {
        cat "$CONFIG" | sed "s/CLOUDHOST/$HOSTNAME/g"
    } > "$USERDATA"
    cp "$USERDATA" "$TMPDIR/nocloud/user-data"
else
    # Replace CLOUDHOST with a default if no hostname provided
    sed "s/CLOUDHOST/firecracker-vm/g" "$CONFIG" > "$TMPDIR/nocloud/user-data"
fi

# Create ISO using mkisofs
mkisofs -output "$OUTPUT" -V CIDATA -J -R "$TMPDIR" > /dev/null 2>&1

echo "Created cloud-init ISO: $OUTPUT"
