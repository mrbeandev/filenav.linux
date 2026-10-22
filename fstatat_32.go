//go:build linux && (386 || arm)

package main

import "syscall"

// 32-bit Linux: fstatat64 fills the 64-bit Stat_t layout Go uses there.
const sysFstatat = syscall.SYS_FSTATAT64
