package render
import ("os";"testing";"github.com/heinanca/truce/internal/recommend";"github.com/heinanca/truce/internal/model")
func p32(v int32)*int32{return &v}
func TestDemoSafe(t *testing.T){
  // telemetry: cpu HPA 50%, peak util 216% at 100m → real peak 216m. VPA says 25m (would slam ceiling).
  tm := model.HPAMetric{SourceType:model.MetricResource,TargetType:model.TargetUtilization,ResourceName:model.ResourceCPU,TargetUtilization:p32(50),PeakUtilization:p32(216)}
  tel := model.WorkloadAnalysis{Workload:model.Workload{Kind:model.KindDeployment,Name:"telemetry"},HPA:model.HPAInfo{Present:true,MinReplicas:3,MaxReplicas:10,Metrics:[]model.HPAMetric{tm}},
    Containers:[]model.ContainerAnalysis{{Name:"telemetry",Requests:model.Resources{CPUMilli:model.Int64(100),MemBytes:model.Int64(1<<30)},HasVPA:true,VPA:model.VPARec{Target:model.Resources{CPUMilli:model.Int64(25),MemBytes:model.Int64(776*1024*1024)}},PeakCPUUsage:model.Int64(216),PeakMemWorkingSet:model.Int64(900*1024*1024)}},
    Verdict:model.VerdictHitsCeiling,CurrentReplicas:3,PredictedReplicas:10,FootprintDelta:model.Delta{CPUMilli:-50,MemBytes:4<<30}}
  // consent: decoupled (KEDA-ish), VPA wants 25m but real peak 80m.
  con := model.WorkloadAnalysis{Workload:model.Workload{Kind:model.KindDeployment,Name:"consent"},HPA:model.HPAInfo{Present:true},
    Containers:[]model.ContainerAnalysis{{Name:"consent",Requests:model.Resources{CPUMilli:model.Int64(100),MemBytes:model.Int64(768*1024*1024)},HasVPA:true,VPA:model.VPARec{Target:model.Resources{CPUMilli:model.Int64(25),MemBytes:model.Int64(599*1024*1024)}},PeakCPUUsage:model.Int64(80),PeakMemWorkingSet:model.Int64(620*1024*1024)}},
    Verdict:model.VerdictDecoupled,CurrentReplicas:1,PredictedReplicas:1}
  pal := NewPalette(true)
  RenderRecommendation(os.Stdout, recommend.For(tel), pal)
  os.Stdout.WriteString("\n")
  RenderRecommendation(os.Stdout, recommend.For(con), pal)
}
