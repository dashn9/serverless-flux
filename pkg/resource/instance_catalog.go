package scaler

import (
	"fmt"
	"math"
	"sort"

	"flux/pkg/config"
)

// selectInstanceType picks the tightest-fit entry from the operator-configured
// node_types list. The caller specifies minimum resource requirements; this
// function returns the instance type whose vCPU + memory most closely meets
// those requirements without going under.
//
// By keeping the mapping in config (instance_type + vcpus + memory_gb), the
// operator controls exactly which types are allowed to be launched — no
// hardcoded catalog, no live API queries at selection time.
func selectInstanceType(nodeTypes []config.NodeTypeConfig, required NodeResources) (string, error) {
	if required.VCPUs <= 0 || required.MemoryGB <= 0 {
		return "", fmt.Errorf("invalid resource requirements: vcpus=%d memory_gb=%.1f", required.VCPUs, required.MemoryGB)
	}
	if len(nodeTypes) == 0 {
		return "", fmt.Errorf("no node_types configured")
	}

	type candidate struct {
		instanceType string
		score        float64
	}

	var candidates []candidate

	for _, nt := range nodeTypes {
		if nt.VCPUs < required.VCPUs || nt.MemoryGB < required.MemoryGB {
			continue
		}
		excessCPU := float64(nt.VCPUs - required.VCPUs)
		excessMem := nt.MemoryGB - required.MemoryGB
		score := math.Sqrt(excessCPU*excessCPU + excessMem*excessMem)
		candidates = append(candidates, candidate{instanceType: nt.InstanceType, score: score})
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no configured node_type meets requirements: vcpus=%d memory_gb=%.1f", required.VCPUs, required.MemoryGB)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score < candidates[j].score
	})

	return candidates[0].instanceType, nil
}
