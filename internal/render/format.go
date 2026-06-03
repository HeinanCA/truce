package render

import (
	"fmt"
	"strings"

	"github.com/heinanca/truce/internal/model"
)

// cpuStr formats CPU milli-cores: whole cores as "2", fractional cores as
// "1.50", sub-core as "500m", zero as "0".
func cpuStr(milli int64) string {
	switch {
	case milli == 0:
		return "0"
	case milli%1000 == 0:
		return fmt.Sprintf("%d", milli/1000)
	case milli >= 1000:
		return fmt.Sprintf("%.2f", float64(milli)/1000)
	default:
		return fmt.Sprintf("%dm", milli)
	}
}

// memStr formats a byte count using binary units (Ki/Mi/Gi/Ti), trimming a
// trailing ".0".
func memStr(bytes int64) string {
	if bytes == 0 {
		return "0"
	}
	const unit = 1024
	units := []string{"", "Ki", "Mi", "Gi", "Ti", "Pi"}
	f := float64(bytes)
	i := 0
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	s := fmt.Sprintf("%.1f", f)
	s = strings.TrimSuffix(s, ".0")
	return s + units[i]
}

// resourceStr renders a per-replica CPU/memory pair, e.g. "500m / 512Mi", using
// "—" for an unset dimension and overall when nothing is set.
func resourceStr(cpuMilli, memBytes int64, hasCPU, hasMem bool) string {
	if !hasCPU && !hasMem {
		return "—"
	}
	cpu, mem := "—", "—"
	if hasCPU {
		cpu = cpuStr(cpuMilli)
	}
	if hasMem {
		mem = memStr(memBytes)
	}
	return cpu + " / " + mem
}

// signedCPU formats a signed CPU delta with an explicit "+"/"-".
func signedCPU(milli int64) string {
	if milli < 0 {
		return "-" + cpuStr(-milli)
	}
	if milli > 0 {
		return "+" + cpuStr(milli)
	}
	return "0"
}

// signedMem formats a signed memory delta with an explicit "+"/"-".
func signedMem(bytes int64) string {
	if bytes < 0 {
		return "-" + memStr(-bytes)
	}
	if bytes > 0 {
		return "+" + memStr(bytes)
	}
	return "0"
}

// deltaStr renders a footprint delta as "cpu / mem", colored green for a net
// reduction and red for growth (per dimension).
func deltaStr(d model.Delta, p Palette) string {
	return colorSign(d.CPUMilli, signedCPU(d.CPUMilli), p) + " / " +
		colorSign(d.MemBytes, signedMem(d.MemBytes), p)
}

func colorSign(v int64, s string, p Palette) string {
	switch {
	case v < 0:
		return p.Green(s) // savings
	case v > 0:
		return p.Red(s) // growth
	default:
		return p.Dim(s)
	}
}

// verdictStr colors a verdict by severity.
func verdictStr(v model.Verdict, p Palette) string {
	switch v {
	case model.VerdictScaleOut, model.VerdictHitsCeiling, model.VerdictOOMRisk:
		return p.Red(string(v))
	case model.VerdictScaleIn:
		return p.Yellow(string(v))
	case model.VerdictSafe:
		return p.Green(string(v))
	default: // DECOUPLED, NO HPA
		return p.Dim(string(v))
	}
}

// flagsStr joins advisory flags; "—" when none.
func flagsStr(flags []model.Flag, p Palette) string {
	if len(flags) == 0 {
		return "—"
	}
	parts := make([]string, len(flags))
	for i, f := range flags {
		parts[i] = p.Yellow(string(f))
	}
	return strings.Join(parts, ",")
}

// --- per-replica request math (mirrors engine.footprint, for display) -------

// perReplicaOld sums the current per-replica request across containers.
func perReplicaOld(cs []model.ContainerAnalysis) (cpu, mem int64, hasCPU, hasMem bool) {
	for _, c := range cs {
		if v, ok := c.Requests.CPU(); ok {
			cpu += v
			hasCPU = true
		}
		if v, ok := c.Requests.Mem(); ok {
			mem += v
			hasMem = true
		}
	}
	return
}

// perReplicaNew sums the per-replica request after applying VPA targets where
// present (unchanged containers keep their current request).
func perReplicaNew(cs []model.ContainerAnalysis) (cpu, mem int64, hasCPU, hasMem bool) {
	for _, c := range cs {
		ccpu, okc := c.Requests.CPU()
		cmem, okm := c.Requests.Mem()
		if c.HasVPA {
			if v, ok := c.VPA.Target.CPU(); ok {
				ccpu, okc = v, true
			}
			if v, ok := c.VPA.Target.Mem(); ok {
				cmem, okm = v, true
			}
		}
		if okc {
			cpu += ccpu
			hasCPU = true
		}
		if okm {
			mem += cmem
			hasMem = true
		}
	}
	return
}
