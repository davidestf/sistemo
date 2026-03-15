// Package main is the entry point for the Sistemo self-hosted daemon and CLI.
// Usage:
//   sistemo up              — start the daemon (HTTP API)
//   sistemo vm list         — list VMs
//   sistemo vm deploy <image> — create a VM
//   sistemo install         — create ~/.sistemo and print kvm/systemd instructions
package main

func main() {
	Execute()
}
