// Package model holds pure data types shared across truce. It imports no
// Kubernetes packages: the collect layer converts resource.Quantity values into
// the plain int64 fields here (CPU as milli-cores, memory as bytes) so that the
// engine can operate on simple arithmetic with no cluster dependency.
package model

// Resources is a CPU/memory pair. A nil pointer means the value was not set
// (e.g. a container with no request for that resource). This distinction drives
// the UNRELIABLE verdict flag, so "unset" must never be conflated with zero.
type Resources struct {
	CPUMilli *int64 // milli-cores; nil = unset
	MemBytes *int64 // bytes; nil = unset
}

// NewResources builds a Resources from optional values. Pass nil to leave a
// dimension unset.
func NewResources(cpuMilli, memBytes *int64) Resources {
	return Resources{CPUMilli: cpuMilli, MemBytes: memBytes}
}

// HasCPU reports whether the CPU dimension is set.
func (r Resources) HasCPU() bool { return r.CPUMilli != nil }

// HasMem reports whether the memory dimension is set.
func (r Resources) HasMem() bool { return r.MemBytes != nil }

// CPU returns the CPU value and whether it was set.
func (r Resources) CPU() (int64, bool) {
	if r.CPUMilli == nil {
		return 0, false
	}
	return *r.CPUMilli, true
}

// Mem returns the memory value and whether it was set.
func (r Resources) Mem() (int64, bool) {
	if r.MemBytes == nil {
		return 0, false
	}
	return *r.MemBytes, true
}

// Int64 is a small helper to take the address of a literal when constructing
// Resources, keeping call sites readable.
func Int64(v int64) *int64 { return &v }
