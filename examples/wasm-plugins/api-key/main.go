//go:build wasip1

// An Odin WebAssembly plugin, in Go: it lets a request through only with a known API key,
// passes the key's owner to the upstream instead of the key, and marks the response.
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o api-key.wasm ./examples/wasm-plugins/api-key
//
// Configuration (wasm.plugins[].config):
//
//	header: X-API-Key                  # where the key is sent (default X-API-Key)
//	keys: {k-123: mobile-app, k-456: partner-acme}   # key -> owner
package main

import (
	"encoding/json"
	"unsafe"
)

type input struct {
	Request struct {
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
	} `json:"request"`
	Config struct {
		Header string            `json:"header"`
		Keys   map[string]string `json:"keys"`
	} `json:"config"`
}

type output struct {
	Action          string            `json:"action"`
	Status          int               `json:"status,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Body            string            `json:"body,omitempty"`
	SetHeaders      map[string]string `json:"setHeaders,omitempty"`
	RemoveHeaders   []string          `json:"removeHeaders,omitempty"`
	ResponseHeaders map[string]string `json:"responseHeaders,omitempty"`
}

// The host writes the input into inBuf (odin_alloc) and reads the result from outBuf. Both are
// package variables, so the garbage collector keeps them while the host uses them.
var inBuf, outBuf []byte

//go:wasmimport env log
func hostLog(ptr unsafe.Pointer, size uint32)

func logf(msg string) {
	if msg != "" {
		hostLog(unsafe.Pointer(unsafe.StringData(msg)), uint32(len(msg)))
	}
}

//go:wasmexport odin_alloc
func odinAlloc(size uint32) uint32 {
	inBuf = make([]byte, size+1) // +1: a valid address even for empty input
	return uint32(uintptr(unsafe.Pointer(&inBuf[0])))
}

//go:wasmexport odin_handle
func odinHandle(ptr, size uint32) uint64 {
	var in input
	var out output
	if err := json.Unmarshal(inBuf[:size], &in); err != nil {
		out = output{Action: "respond", Status: 500, Body: `{"message":"bad plugin input"}`}
	} else {
		out = decide(&in)
	}
	outBuf, _ = json.Marshal(out)
	return uint64(uintptr(unsafe.Pointer(&outBuf[0])))<<32 | uint64(len(outBuf))
}

func decide(in *input) output {
	header := in.Config.Header
	if header == "" {
		header = "X-API-Key"
	}
	key := ""
	for k, v := range in.Request.Headers {
		if equalFold(k, header) {
			key = v
		}
	}
	owner, ok := in.Config.Keys[key]
	if key == "" || !ok {
		logf("rejected " + in.Request.Method + " " + in.Request.Path + ": missing or unknown API key")
		return output{
			Action:  "respond",
			Status:  401,
			Headers: map[string]string{"Content-Type": "application/json", "WWW-Authenticate": "ApiKey header=" + header},
			Body:    `{"message":"missing or unknown API key"}`,
		}
	}
	return output{
		Action:          "continue",
		SetHeaders:      map[string]string{"X-Consumer": owner},
		RemoveHeaders:   []string{header},
		ResponseHeaders: map[string]string{"X-Checked-By": "wasm-api-key"},
	}
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

func main() {}
