package e2e

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// TestNetworkDenied proves that the network-deny run works: under
// scripts/ci/nonet-exec.sh every socket operation fails with a permission
// error. It runs only with PARITYDESK_NONET_PROBE=1, which the CI step
// sets together with go test -exec scripts/ci/nonet-exec.sh. It touches
// only the loopback interface, so it sends nothing off the machine even
// when the sandbox is missing. It uses raw syscalls, not package net, so
// that net stays out of the dependency graph of the tests too.
func TestNetworkDenied(t *testing.T) {
	if os.Getenv("PARITYDESK_NONET_PROBE") != "1" {
		t.Skip("set PARITYDESK_NONET_PROBE=1 and run under scripts/ci/nonet-exec.sh")
	}
	loopback := func(port int) *syscall.SockaddrInet4 {
		return &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}
	}
	// Listen on a free loopback port.
	err := withSocket(syscall.SOCK_STREAM, func(fd int) error {
		if err := syscall.Bind(fd, loopback(0)); err != nil {
			return err
		}
		return syscall.Listen(fd, 1)
	})
	if err == nil {
		t.Fatal("listening on loopback succeeded; the sandbox does not deny the network")
	}
	if !denied(err) {
		t.Fatalf("listen failed, but not with a permission error: %v", err)
	}
	// Port 9 (discard) on loopback: without a sandbox a TCP connect is
	// refused or accepted, never denied.
	err = withSocket(syscall.SOCK_STREAM, func(fd int) error { return syscall.Connect(fd, loopback(9)) })
	if err == nil {
		t.Fatal("connecting to loopback succeeded; the sandbox does not deny the network")
	}
	if !denied(err) {
		t.Fatalf("connect failed, but not with a permission error: %v", err)
	}
	// A UDP connect sends nothing; without a sandbox it succeeds.
	err = withSocket(syscall.SOCK_DGRAM, func(fd int) error { return syscall.Connect(fd, loopback(9)) })
	if err == nil {
		t.Fatal("a UDP socket was connected; the sandbox does not deny the network")
	}
	if !denied(err) {
		t.Fatalf("UDP connect failed, but not with a permission error: %v", err)
	}
}

// withSocket opens an IPv4 socket of the given type, runs f on it and
// closes it. A failure to create the socket is returned as is.
func withSocket(typ int, f func(fd int) error) error {
	fd, err := syscall.Socket(syscall.AF_INET, typ, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	return f(fd)
}

// denied reports a sandbox refusal (EPERM or EACCES).
func denied(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
}
