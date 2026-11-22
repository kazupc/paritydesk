#!/bin/sh
# Prints the packages whose tests run on Linux. paritydesk ships for macOS
# only, but most packages are portable; the Linux job runs their tests
# with the race detector. The packages listed below drive the real
# filesystem layer (fsx.Default), which is macOS-only, so their tests
# need macOS. A new package is included by default.
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd -P)
cd "$root"
module=$(go list -m)
go list ./... | grep -v -x \
	-e "$module/internal/cli" \
	-e "$module/internal/orchestrator"
