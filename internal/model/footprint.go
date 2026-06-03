package model

// Footprint is a total resource reservation: replicas multiplied by per-replica
// requests, summed into CPU milli-cores and memory bytes.
type Footprint struct {
	CPUMilli int64
	MemBytes int64
}

// Delta is the change in footprint from applying a recommendation:
// predicted_replicas*R_new - current_replicas*R_old, per resource. Negative
// means a reduction (savings); positive means growth (a possible backfire when
// the trigger was a downsizing rec).
type Delta struct {
	CPUMilli int64
	MemBytes int64
}

// Sub returns the delta of new minus old.
func Sub(newF, oldF Footprint) Delta {
	return Delta{
		CPUMilli: newF.CPUMilli - oldF.CPUMilli,
		MemBytes: newF.MemBytes - oldF.MemBytes,
	}
}

// Add returns the element-wise sum of two deltas, for footer roll-ups.
func (d Delta) Add(other Delta) Delta {
	return Delta{
		CPUMilli: d.CPUMilli + other.CPUMilli,
		MemBytes: d.MemBytes + other.MemBytes,
	}
}
