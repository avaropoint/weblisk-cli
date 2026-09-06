//go:build windows

package platform

import "unsafe"

// unsafePointerTo exists so the syscall above reads as one line.
//
// Confined to its own file so `unsafe` appears in exactly one place in this
// program and is trivial to audit: it converts the address of a local uint32
// into the out-parameter GetExitCodeProcess writes through.
func unsafePointerTo(v *uint32) unsafe.Pointer { return unsafe.Pointer(v) }
