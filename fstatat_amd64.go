//go:build linux && amd64

package main

import "syscall"

const sysFstatat64bit = syscall.SYS_NEWFSTATAT
