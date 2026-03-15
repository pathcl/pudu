KERNEL_URL      := https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.10/x86_64/vmlinux-5.10.223
ROOTFS_URL      := https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.10/x86_64/ubuntu-22.04.ext4
FC_VERSION      := v1.10.1
FC_URL          := https://github.com/firecracker-microvm/firecracker/releases/download/$(FC_VERSION)/firecracker-$(FC_VERSION)-x86_64.tgz
FC_INSTALL_DIR  := /usr/local/bin

KERNEL      := vmlinux.bin
ROOTFS      := rootfs.ext4
BINARY      := pudu
CLIENT_BIN  := puduc/puduc
AGENT_BIN   := pudu-agent
CLOUD_INIT_ISO := cloud-init.iso
CLOUD_INIT_CONFIG := cloud-init-config.yaml

TAP_DEV     := tap0
TAP_IP      := 172.16.0.1
VM_IP       := 172.16.0.2
VM_MASK     := 255.255.255.0
VM_MAC      := AA:FC:00:00:00:01
# Detect default host interface for NAT (first non-loopback default route)
HOST_IFACE  := $(shell ip route get 8.8.8.8 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($$i=="dev") print $$(i+1)}' | head -1)

KERNEL_ARGS := "console=ttyS0 reboot=k panic=1 pci=off ip=$(VM_IP)::$(TAP_IP):$(VM_MASK)::eth0:off:8.8.8.8"

# Multi-VM configuration
N           ?= 3

.PHONY: build build-puduc agent run assets net-up net-down clean net-up-multi net-down-multi run-multi serve cloud-init-iso update cleanup scenario server deps

deps:
	@echo "==> Installing firecracker $(FC_VERSION)"
	@TMPDIR=$$(mktemp -d) && \
	  curl -fsSL -o $$TMPDIR/fc.tgz $(FC_URL) && \
	  tar -xzf $$TMPDIR/fc.tgz -C $$TMPDIR && \
	  sudo install -m 0755 $$TMPDIR/release-$(FC_VERSION)-x86_64/firecracker-$(FC_VERSION)-x86_64 $(FC_INSTALL_DIR)/firecracker && \
	  rm -rf $$TMPDIR
	@echo "  ✓ firecracker installed: $$(firecracker --version | head -1)"
	@echo "==> Installing system dependencies"
	sudo apt-get install -y --no-install-recommends \
	  cloud-image-utils \
	  e2fsprogs \
	  iproute2 \
	  iptables
	@echo "  ✓ system dependencies installed"

build:
	go build -o $(BINARY) .
	go build -o $(AGENT_BIN) ./agent/
	go build -o $(CLIENT_BIN) ./puduc/

build-puduc:
	go build -o $(CLIENT_BIN) ./puduc/

agent: $(AGENT_BIN)

$(AGENT_BIN):
	go build -o $(AGENT_BIN) ./agent/

$(KERNEL):
	@echo "==> Downloading kernel (Firecracker CI v1.10, 5.10.223)"
	curl -fsSL -o $(KERNEL) $(KERNEL_URL)

$(ROOTFS): $(AGENT_BIN)
	@echo "==> Downloading Ubuntu 22.04 rootfs (Firecracker CI v1.10)"
	curl -fsSL -o $(ROOTFS) $(ROOTFS_URL)
	@echo "==> Setting up rootfs (SSH, root password, agent)"
	chmod +x setup-rootfs.sh
	./setup-rootfs.sh $(ROOTFS) $(AGENT_BIN)

$(CLOUD_INIT_ISO): $(CLOUD_INIT_CONFIG)
	@echo "==> Creating cloud-init ISO"
	chmod +x make-cloud-init-iso.sh
	./make-cloud-init-iso.sh $(CLOUD_INIT_ISO) $(CLOUD_INIT_CONFIG)

update:
	@rm -f $(CLOUD_INIT_ISO)
	@$(MAKE) $(CLOUD_INIT_ISO)

assets: $(KERNEL) $(ROOTFS) $(CLOUD_INIT_ISO)

net-up:
	@echo "==> Setting up TAP networking ($(TAP_DEV), host=$(TAP_IP), vm=$(VM_IP))"
	sudo ip tuntap add $(TAP_DEV) mode tap 2>/dev/null || true
	sudo ip addr add $(TAP_IP)/24 dev $(TAP_DEV) 2>/dev/null || true
	sudo ip link set $(TAP_DEV) up
	sudo sh -c 'echo 1 > /proc/sys/net/ipv4/ip_forward'
	sudo iptables -t nat -C POSTROUTING -o $(HOST_IFACE) -j MASQUERADE 2>/dev/null || \
	  sudo iptables -t nat -A POSTROUTING -o $(HOST_IFACE) -j MASQUERADE
	sudo iptables -C FORWARD -i $(TAP_DEV) -o $(HOST_IFACE) -j ACCEPT 2>/dev/null || \
	  sudo iptables -A FORWARD -i $(TAP_DEV) -o $(HOST_IFACE) -j ACCEPT
	sudo iptables -C FORWARD -i $(HOST_IFACE) -o $(TAP_DEV) -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || \
	  sudo iptables -A FORWARD -i $(HOST_IFACE) -o $(TAP_DEV) -m state --state RELATED,ESTABLISHED -j ACCEPT

net-down:
	@echo "==> Tearing down TAP networking"
	sudo ip link del $(TAP_DEV) 2>/dev/null || true
	sudo iptables -t nat -D POSTROUTING -o $(HOST_IFACE) -j MASQUERADE 2>/dev/null || true
	sudo iptables -D FORWARD -i $(TAP_DEV) -o $(HOST_IFACE) -j ACCEPT 2>/dev/null || true
	sudo iptables -D FORWARD -i $(HOST_IFACE) -o $(TAP_DEV) -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true

run: assets net-up
	sudo ./$(BINARY) run \
	  --kernel $(KERNEL) \
	  --rootfs $(ROOTFS) \
	  --mem 512  \
	  --tap $(TAP_DEV) \
	  --mac $(VM_MAC) \
	  --kernel-args $(KERNEL_ARGS) \
	  --cloud-init-iso $(CLOUD_INIT_ISO)

net-up-multi:
	@for i in $$(seq 0 $$(($(N)-1))); do \
		TAP=tap$$i; \
		HOST_IP=172.16.$$i.1; \
		echo "==> Setting up TAP $$TAP (host=$$HOST_IP/30)"; \
		sudo ip tuntap add $$TAP mode tap 2>/dev/null || true; \
		sudo ip addr add $$HOST_IP/30 dev $$TAP 2>/dev/null || true; \
		sudo ip link set $$TAP up; \
		sudo iptables -t nat -C POSTROUTING -o $(HOST_IFACE) -j MASQUERADE 2>/dev/null || \
		  sudo iptables -t nat -A POSTROUTING -o $(HOST_IFACE) -j MASQUERADE; \
		sudo iptables -C FORWARD -i $$TAP -o $(HOST_IFACE) -j ACCEPT 2>/dev/null || \
		  sudo iptables -A FORWARD -i $$TAP -o $(HOST_IFACE) -j ACCEPT; \
		sudo iptables -C FORWARD -i $(HOST_IFACE) -o $$TAP -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || \
		  sudo iptables -A FORWARD -i $(HOST_IFACE) -o $$TAP -m state --state RELATED,ESTABLISHED -j ACCEPT; \
	done
	@sudo sh -c 'echo 1 > /proc/sys/net/ipv4/ip_forward'

net-down-multi:
	@for i in $$(seq 0 $$(($(N)-1))); do \
		TAP=tap$$i; \
		echo "==> Tearing down TAP $$TAP"; \
		sudo ip link del $$TAP 2>/dev/null || true; \
		sudo iptables -t nat -D POSTROUTING -o $(HOST_IFACE) -j MASQUERADE 2>/dev/null || true; \
		sudo iptables -D FORWARD -i $$TAP -o $(HOST_IFACE) -j ACCEPT 2>/dev/null || true; \
		sudo iptables -D FORWARD -i $(HOST_IFACE) -o $$TAP -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true; \
	done

ROOTFS_SIZE ?= 1024

run-multi: assets net-up-multi
	sudo ./$(BINARY) run \
	  --kernel $(KERNEL) \
	  --rootfs $(ROOTFS) \
	  --mem 512 \
	  --rootfs-size $(ROOTFS_SIZE) \
	  --count $(N) \
	  --cloud-init-iso $(CLOUD_INIT_ISO)

serve: assets net-up-multi
	sudo ./$(BINARY) serve \
	  --kernel $(KERNEL) \
	  --rootfs $(ROOTFS) \
	  --mem 512 \
	  --rootfs-size $(ROOTFS_SIZE) \
	  --count $(N) \
	  --cloud-init-iso $(CLOUD_INIT_ISO) \
	  --port 8888

SCENARIO    ?= scenarios/monolith/disk-full.yaml

scenario: assets net-up-multi
	sudo ./$(BINARY) scenario run \
	  --kernel $(KERNEL) \
	  --rootfs $(ROOTFS) \
	  --cloud-init-iso $(CLOUD_INIT_ISO) \
	  $(SCENARIO)

server: assets net-up-multi
	sudo ./$(BINARY) server \
	  --kernel $(KERNEL) \
	  --rootfs $(ROOTFS) \
	  --cloud-init-iso $(CLOUD_INIT_ISO) \
	  --port 8888

cleanup: net-down-multi
	@echo "==> Network cleaned up"

clean: net-down
	rm -f $(BINARY) $(CLIENT_BIN) $(AGENT_BIN) $(KERNEL) $(ROOTFS) $(CLOUD_INIT_ISO) cloud-init-*.iso vm-*.log vm-*.ext4 puduc/puduc
