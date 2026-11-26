//go:build wasip1

// A WebAssembly plugin that never returns, to test that plugin timeouts are enforced.
package main

import "unsafe"

var inBuf []byte

//go:wasmexport odin_alloc
func odinAlloc(size uint32) uint32 {
	inBuf = make([]byte, size+1)
	return uint32(uintptr(unsafe.Pointer(&inBuf[0])))
}

var spins uint64

//go:wasmexport odin_handle
func odinHandle(ptr, size uint32) uint64 {
	for {
		spins++
	}
}

func main() {}
