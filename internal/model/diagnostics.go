package model

// ComponentStatus is an honest report on one cluster capability truce depends
// on. Available reflects what was actually observed (not inferred); when false,
// Impact states what truce cannot do and Install gives remediation.
type ComponentStatus struct {
	Name      string
	Available bool
	Detail    string // what was observed
	Impact    string // consequence when unavailable (empty when available)
	Install   string // remediation command/guidance (empty when available)
}

// Diagnostics is the set of capability checks shown in the output header so the
// user knows exactly which inputs were present and which were missing.
type Diagnostics struct {
	Components []ComponentStatus
}

// Degraded reports whether any depended-on component is unavailable.
func (d Diagnostics) Degraded() bool {
	for _, c := range d.Components {
		if !c.Available {
			return true
		}
	}
	return false
}
