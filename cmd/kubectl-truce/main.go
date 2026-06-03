// Command kubectl-truce is a read-only, HPA-aware Kubernetes rightsizing
// advisor. It predicts what a workload's HorizontalPodAutoscaler will do if a
// VerticalPodAutoscaler recommendation is applied, and reports the resulting
// footprint delta. It never writes to the cluster.
//
// Exit codes:
//
//	0  success, no --fail-on match
//	1  operational error (connection, parsing, render)
//	3  --fail-on matched a verdict (CI gate tripped)
//
// It installs as a kubectl plugin (binary kubectl-truce) and also runs
// standalone.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

func main() {
	err := newRootCommand().ExecuteContext(context.Background())
	if err == nil {
		return
	}

	var gate *gateError
	if errors.As(err, &gate) {
		fmt.Fprintln(os.Stderr, "truce:", err)
		os.Exit(3)
	}
	fmt.Fprintln(os.Stderr, "truce:", err)
	os.Exit(1)
}
