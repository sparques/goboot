GOCACHE ?= /tmp
KERNEL ?= /boot/vmlinuz-linux-lts
QEMU_ACCEL ?= tcg
QEMU_DIR ?= .build/qemu
QEMU_INIT ?= goboot
QEMU_MARKER ?= GOBOOT_QEMU_OK

.PHONY: goboot qemu qemu-import clean

goboot:
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -o goboot ./cmd/goboot

clean:
	rm -f goboot
	rm -rf $(QEMU_DIR)
	GOCACHE=$(GOCACHE) go clean

qemu: goboot
	mkdir -p $(QEMU_DIR)/root/etc $(QEMU_DIR)/root/bin $(QEMU_DIR)/root/proc $(QEMU_DIR)/root/dev $(QEMU_DIR)/root/sys $(QEMU_DIR)/root/tmp
	cp $(QEMU_INIT) $(QEMU_DIR)/root/init
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -o $(QEMU_DIR)/root/bin/bootprobe ./cmd/bootprobe
	cp testdata/qemu/config.yaml $(QEMU_DIR)/root/etc/goboot.yaml
	cd $(QEMU_DIR)/root && find . -print0 | LC_ALL=C sort -z | cpio --null -o -H newc > ../initrd.img 2>/dev/null
	@timeout 120s qemu-system-x86_64 -accel $(QEMU_ACCEL) -m 256M -kernel $(KERNEL) -initrd $(QEMU_DIR)/initrd.img -append "console=ttyS0 rdinit=/init" -display none -serial stdio -monitor none -no-reboot > $(QEMU_DIR)/console.log 2>&1; status=$$?; cat $(QEMU_DIR)/console.log; test $$status -eq 0; grep -q 'GOBOOT_QEMU_OK' $(QEMU_DIR)/console.log; grep -q '$(QEMU_MARKER)' $(QEMU_DIR)/console.log

qemu-import:
	mkdir -p .build/qemu-import
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -o .build/qemu-import/imported-init ./testdata/qemu/init
	$(MAKE) qemu QEMU_DIR=.build/qemu-import QEMU_INIT=.build/qemu-import/imported-init QEMU_MARKER=GOBOOT_LIBRARY_RETURNED
