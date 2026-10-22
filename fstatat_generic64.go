//go:build linux && (arm64 || riscv64 || loong64)

package main

import "syscall"

const sysFstatat64bit = syscall.SYS_FSTATAT
