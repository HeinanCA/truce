package engine

import "github.com/heinanca/truce/internal/model"

// footprintDelta computes the headline change in reserved resources from
// applying the recommendation:
//
//	delta = predicted_replicas * R_new  -  current_replicas * R_old
//
// R_old is the current per-replica request summed across containers; R_new uses
// each container's VPA target where present and the unchanged current request
// otherwise. CPU is in milli-cores, memory in bytes. A negative delta is a
// reduction (savings); a positive delta when a downsizing rec was the trigger is
// the backfire the tool exists to surface.
func footprintDelta(containers []model.ContainerAnalysis, currentReplicas, predictedReplicas int32) model.Delta {
	oldPer := perReplica(containers, false)
	newPer := perReplica(containers, true)

	old := model.Footprint{
		CPUMilli: oldPer.CPUMilli * int64(currentReplicas),
		MemBytes: oldPer.MemBytes * int64(currentReplicas),
	}
	next := model.Footprint{
		CPUMilli: newPer.CPUMilli * int64(predictedReplicas),
		MemBytes: newPer.MemBytes * int64(predictedReplicas),
	}
	return model.Sub(next, old)
}

// perReplica sums one replica's CPU/memory request across containers. When
// useVPA is true, a container's VPA target replaces its current request on any
// dimension the VPA recommends; dimensions without a recommendation keep the
// current request. Unset values contribute zero.
func perReplica(containers []model.ContainerAnalysis, useVPA bool) model.Footprint {
	var f model.Footprint
	for _, c := range containers {
		cpu, _ := c.Requests.CPU()
		mem, _ := c.Requests.Mem()
		if useVPA && c.HasVPA {
			if v, ok := c.VPA.Target.CPU(); ok {
				cpu = v
			}
			if v, ok := c.VPA.Target.Mem(); ok {
				mem = v
			}
		}
		f.CPUMilli += cpu
		f.MemBytes += mem
	}
	return f
}
