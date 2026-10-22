//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package main

import "syscall"

// 64-bit Linux: newfstatat (named fstatat on arm64/riscv64/loong64).
const sysFstatat = sysFstatat64bit

var _ = syscall.Stat_t{}
