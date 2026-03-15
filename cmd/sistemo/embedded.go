package main

import _ "embed"

//go:embed build-rootfs.sh
var embeddedBuildScript []byte

//go:embed vm-init.sh
var embeddedVMInit []byte
