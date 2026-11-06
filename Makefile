GOCACHE ?= /tmp
KERNEL ?= /boot/vmlinuz-linux-lts
QEMU_ACCEL ?= tcg
QEMU_DIR ?= .build/qemu
QEMU_INIT ?= goboot
QEMU_MARKER ?= GOBOOT_QEMU_OK
QEMU_CONFIG ?= testdata/qemu/config.yaml
QEMU_EXTRA_ARGS ?=
MODULE_SOURCE ?= /usr/lib/modules/6.18.57-2-lts

.PHONY: goboot qemu qemu-import qemu-root qemu-network qemu-restart qemu-shutdown qemu-orphan qemu-reboot clean

goboot:
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -o goboot ./cmd/goboot

clean:
	rm -f goboot
	rm -rf $(QEMU_DIR)
	GOCACHE=$(GOCACHE) go clean

qemu2: goboot
	echo goboot | cpio -H newc -o > initrd.img
	qemu-system-x86_64 \
		-m 2G \
		-kernel /boot/vmlinuz-linux-lts \
		-initrd ./initrd.img \
		-append "console=ttyS0 rdinit=/goboot earlycon=uart8250,io,0x3f8,115200" \
		-display none \
		-serial stdio \
		-no-reboot


qemu: goboot
	mkdir -p $(QEMU_DIR)/root/etc $(QEMU_DIR)/root/bin $(QEMU_DIR)/root/proc $(QEMU_DIR)/root/dev $(QEMU_DIR)/root/sys $(QEMU_DIR)/root/tmp
	cp $(QEMU_INIT) $(QEMU_DIR)/root/init
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -o $(QEMU_DIR)/root/bin/bootprobe ./cmd/bootprobe
	cp $(QEMU_CONFIG) $(QEMU_DIR)/root/etc/goboot.yaml
	cd $(QEMU_DIR)/root && find . -print0 | LC_ALL=C sort -z | cpio --null -o -H newc > ../initrd.img 2>/dev/null
	timeout 120s qemu-system-x86_64 \
		-accel $(QEMU_ACCEL) \
		-m 2G \
		-kernel $(KERNEL) \
		-initrd $(QEMU_DIR)/initrd.img \
		-append "console=ttyS0 rdinit=/init -- -config /etc/goboot.yaml" \
		-display none \
		-serial stdio \
		-monitor none -no-reboot \
		$(QEMU_EXTRA_ARGS) > $(QEMU_DIR)/console.log 2>&1; status=$$?; cat $(QEMU_DIR)/console.log; test $$status -eq 0; grep -q 'GOBOOT_QEMU_OK' $(QEMU_DIR)/console.log; grep -q '$(QEMU_MARKER)' $(QEMU_DIR)/console.log

qemu-import:
	mkdir -p .build/qemu-import
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -o .build/qemu-import/imported-init ./testdata/qemu/init
	$(MAKE) qemu QEMU_DIR=.build/qemu-import QEMU_INIT=.build/qemu-import/imported-init QEMU_MARKER=GOBOOT_LIBRARY_RETURNED

qemu-root:
	mkdir -p .build/qemu-root/final-root/bin .build/qemu-root/final-root/etc
	printf 'goboot-final-root\n' > .build/qemu-root/final-root/etc/goboot-root-marker
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -o .build/qemu-root/final-root/bin/bootprobe ./cmd/bootprobe
	truncate -s 64M .build/qemu-root/root.img
	mkfs.ext4 -F -q -d .build/qemu-root/final-root .build/qemu-root/root.img
	$(MAKE) qemu QEMU_DIR=.build/qemu-root QEMU_CONFIG=testdata/qemu/root-config.yaml QEMU_EXTRA_ARGS="-drive file=.build/qemu-root/root.img,format=raw,if=virtio"

qemu-network:
	mkdir -p .build/qemu-network/root/modules/kernel/drivers/net .build/qemu-network/root/modules/kernel/net/core
	cp $(MODULE_SOURCE)/kernel/drivers/net/virtio_net.ko.zst .build/qemu-network/root/modules/kernel/drivers/net/
	cp $(MODULE_SOURCE)/kernel/drivers/net/net_failover.ko.zst .build/qemu-network/root/modules/kernel/drivers/net/
	cp $(MODULE_SOURCE)/kernel/net/core/failover.ko.zst .build/qemu-network/root/modules/kernel/net/core/
	printf '%s\n' 'kernel/drivers/net/virtio_net.ko.zst: kernel/drivers/net/net_failover.ko.zst kernel/net/core/failover.ko.zst' 'kernel/drivers/net/net_failover.ko.zst: kernel/net/core/failover.ko.zst' 'kernel/net/core/failover.ko.zst:' > .build/qemu-network/root/modules/modules.dep
	$(MAKE) qemu QEMU_DIR=.build/qemu-network QEMU_CONFIG=testdata/qemu/network-config.yaml QEMU_EXTRA_ARGS="-netdev user,id=net0 -device virtio-net-pci,netdev=net0"

qemu-restart:
	$(MAKE) qemu QEMU_DIR=.build/qemu-restart QEMU_CONFIG=testdata/qemu/restart-config.yaml QEMU_MARKER=GOBOOT_RESTART_FIRST_EXIT

qemu-shutdown:
	$(MAKE) qemu QEMU_DIR=.build/qemu-shutdown QEMU_CONFIG=testdata/qemu/shutdown-config.yaml QEMU_MARKER=GOBOOT_SERVICE_TERMINATED

qemu-orphan:
	$(MAKE) qemu QEMU_DIR=.build/qemu-orphan QEMU_CONFIG=testdata/qemu/orphan-config.yaml QEMU_MARKER=GOBOOT_ORPHAN_REAPED

qemu-reboot:
	$(MAKE) qemu QEMU_DIR=.build/qemu-reboot QEMU_CONFIG=testdata/qemu/reboot-config.yaml QEMU_MARKER="reboot: Restarting system"
