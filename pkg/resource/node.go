package scaler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"flux/pkg/client"
	"flux/pkg/config"
	"flux/pkg/models"
	"flux/pkg/registry"
)

// smallestNodeResources returns the NodeResources of the entry in nodeTypes
// with the fewest vCPUs (breaking ties by least memory). This is used as the
// minimum requirement passed to SpawnNode so the provider selects the
// tightest-fit configured type.
func smallestNodeResources(nodeTypes []config.NodeTypeConfig) NodeResources {
	if len(nodeTypes) == 0 {
		return NodeResources{VCPUs: 1, MemoryGB: 1}
	}
	smallest := nodeTypes[0]
	for _, nt := range nodeTypes[1:] {
		if nt.VCPUs < smallest.VCPUs || (nt.VCPUs == smallest.VCPUs && nt.MemoryGB < smallest.MemoryGB) {
			smallest = nt
		}
	}
	return NodeResources{VCPUs: smallest.VCPUs, MemoryGB: smallest.MemoryGB}
}

// Autoscaler monitors agent resource pressure and triggers scale-up events
// when CPU or memory load is sustained above the configured thresholds.
type Autoscaler struct {
	registry    *registry.Registry
	agentClient *client.AgentClient
	scaler      CloudProvider
	cfg         *config.AutoscaleConfig
	agentPort   int

	// pressureHistory tracks recent CPU and memory samples per agent.
	mu              sync.Mutex
	pressureHistory map[string][]pressureSample

	lastScaleUp time.Time
}

type pressureSample struct {
	cpuPercent float64
	memPercent float64
	at         time.Time
}

// NewAutoscaler creates an autoscaler from the given config.
// Returns nil if autoscaling is not enabled.
func NewAutoscaler(
	reg *registry.Registry,
	agentClient *client.AgentClient,
	cfg *config.AutoscaleConfig,
	agentPort int,
	redisAddr string,
	tlsCfg *config.TLSConfig,
) (*Autoscaler, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}

	var provider CloudProvider

	switch cfg.Provider {
	case "aws":
		if cfg.AWS == nil {
			log.Printf("[autoscaler] AWS provider selected but no AWS config provided, disabling")
			return nil, nil
		}
		p, err := NewAWSProvider(cfg.AWS, cfg, agentPort, redisAddr, tlsCfg)
		if err != nil {
			return nil, err
		}
		provider = p
	default:
		log.Printf("[autoscaler] Unknown provider %q, disabling autoscaler", cfg.Provider)
		return nil, nil
	}

	return &Autoscaler{
		registry:        reg,
		agentClient:     agentClient,
		scaler:          provider,
		cfg:             cfg,
		agentPort:       agentPort,
		pressureHistory: make(map[string][]pressureSample),
	}, nil
}

// Start begins the monitoring loop in a background goroutine.
func (a *Autoscaler) Start(ctx context.Context) {
	pollInterval := time.Duration(a.cfg.PollIntervalSec) * time.Second

	log.Printf("[autoscaler] Started (provider=%s name=%s) — cpu_upper=%.0f%% mem_upper=%.0f%% window=%ds cooldown=%ds max_nodes=%d poll=%ds",
		a.cfg.Provider, a.cfg.Name,
		a.cfg.CPUUpperThreshold, a.cfg.MemUpperThreshold,
		a.cfg.EvaluationWindowSec, a.cfg.CooldownSec, a.cfg.MaxNodes, a.cfg.PollIntervalSec)

	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Printf("[autoscaler] Stopped")
				return
			case <-ticker.C:
				a.poll(ctx)
			}
		}
	}()
}

// poll fetches metrics from all agents and evaluates whether to scale.
func (a *Autoscaler) poll(ctx context.Context) {
	// Only poll agents that are fully online (not pre-registered).
	agents := a.registry.GetOnlineAgents()
	if len(agents) == 0 {
		return
	}

	now := time.Now()
	window := time.Duration(a.cfg.EvaluationWindowSec) * time.Second

	for _, agent := range agents {
		status, err := a.agentClient.GetNodeStatus(agent)
		if err != nil {
			log.Printf("[autoscaler] Failed to get status from %s: %v", agent.ID, err)
			continue
		}

		// Update registry with latest status
		a.registry.UpdateNodeStatus(agent.ID, status)

		// Record CPU + memory sample.
		a.mu.Lock()
		a.pressureHistory[agent.ID] = append(a.pressureHistory[agent.ID], pressureSample{
			cpuPercent: status.CPUPercent,
			memPercent: status.MemPercent,
			at:         now,
		})
		// Trim samples older than the evaluation window.
		trimmed := a.pressureHistory[agent.ID][:0]
		for _, s := range a.pressureHistory[agent.ID] {
			if now.Sub(s.at) <= window {
				trimmed = append(trimmed, s)
			}
		}
		a.pressureHistory[agent.ID] = trimmed
		a.mu.Unlock()
	}

	// Evaluate sustained pressure across all agents
	if a.shouldScaleUp(agents, now, window) {
		a.triggerScaleUp(ctx, agents)
	}
}

// shouldScaleUp returns true when all online agents have had sustained pressure
// above threshold for the full evaluation window on CPU *or* memory — whichever
// is the higher-pressure resource triggers the decision.
func (a *Autoscaler) shouldScaleUp(agents []*models.Agent, now time.Time, window time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(agents) == 0 {
		return false
	}

	for _, agent := range agents {
		history, ok := a.pressureHistory[agent.ID]
		if !ok || len(history) == 0 {
			return false
		}

		// Require samples spanning the full window before deciding.
		if now.Sub(history[0].at) < window {
			return false
		}

		// Every sample in the window must be above the CPU *or* memory threshold.
		// Both metrics are treated equally: either sustained high is enough to scale.
		allHigh := true
		for _, s := range history {
			if s.cpuPercent < a.cfg.CPUUpperThreshold && s.memPercent < a.cfg.MemUpperThreshold {
				allHigh = false
				break
			}
		}
		if !allHigh {
			return false
		}
	}

	return true
}

// triggerScaleUp attempts to provision a new node if cooldown and max-node
// constraints are satisfied.
func (a *Autoscaler) triggerScaleUp(ctx context.Context, agents []*models.Agent) {
	cooldown := time.Duration(a.cfg.CooldownSec) * time.Second

	// Check cooldown
	if time.Since(a.lastScaleUp) < cooldown {
		log.Printf("[autoscaler] Scale-up skipped: cooldown active (%.0fs remaining)",
			(cooldown - time.Since(a.lastScaleUp)).Seconds())
		return
	}

	// Check max nodes (all nodes, including pre-registered)
	allAgents := a.registry.GetAllAgents()
	if len(allAgents) >= a.cfg.MaxNodes {
		log.Printf("[autoscaler] Scale-up skipped: already at max nodes (%d/%d)", len(allAgents), a.cfg.MaxNodes)
		return
	}

	// Log the pressure that triggered this.
	a.mu.Lock()
	var sumCPU, sumMem float64
	var count int
	for _, agent := range agents {
		if history, ok := a.pressureHistory[agent.ID]; ok && len(history) > 0 {
			last := history[len(history)-1]
			sumCPU += last.cpuPercent
			sumMem += last.memPercent
			count++
		}
	}
	a.mu.Unlock()
	if count > 0 {
		sumCPU /= float64(count)
		sumMem /= float64(count)
	}

	log.Printf("[autoscaler] Scaling up! Avg CPU=%.1f%% Mem=%.1f%% across %d agents (cpu_threshold=%.0f%% mem_threshold=%.0f%%)",
		sumCPU, sumMem, len(agents), a.cfg.CPUUpperThreshold, a.cfg.MemUpperThreshold)

	// Resolve the minimum resource requirement from the smallest configured
	// node type. SpawnNode will pick the tightest fit from node_types.
	resources := smallestNodeResources(a.cfg.NodeTypes)

	node, err := a.scaler.SpawnNode(ctx, resources)
	if err != nil {
		log.Printf("[autoscaler] Scale-up failed (spawn): %v", err)
		return
	}

	a.lastScaleUp = time.Now()

	// Pre-register the node immediately so the fleet count is accurate
	// while bootstrap is still in progress.
	agentAddr := fmt.Sprintf("%s:%d", node.PrivateIP, a.agentPort)
	a.registry.PreRegisterAgent(node.AgentID, agentAddr, a.cfg.MaxConcurrency, node.ProviderID)

	log.Printf("[autoscaler] Node spawned: agent=%s addr=%s provider_id=%s — bootstrapping...",
		node.AgentID, agentAddr, node.ProviderID)

	// Bootstrap the node asynchronously so we don't block the poll loop.
	go func() {
		if err := a.scaler.Bootstrap(ctx, node); err != nil {
			log.Printf("[autoscaler] Bootstrap failed for %s: %v", node.AgentID, err)
			return
		}
		log.Printf("[autoscaler] Scale-up complete: agent=%s (total nodes: %d)",
			node.AgentID, len(a.registry.GetAllAgents()))
	}()

	// Clear pressure history since the fleet composition changed.
	a.mu.Lock()
	a.pressureHistory = make(map[string][]pressureSample)
	a.mu.Unlock()
}
