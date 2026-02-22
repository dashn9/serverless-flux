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

type ScaleDirection = int

const (
	ScaleDirectionUp ScaleDirection = iota
	ScaleDirectionNeutral
	ScaleDirectionDown
)

// smallestNodeResources returns the NodeResources of the smallest configured node type.
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

// largestNodeResources returns the NodeResources of the largest configured node type.
func largestNodeResources(nodeTypes []config.NodeTypeConfig) NodeResources {
	if len(nodeTypes) == 0 {
		return NodeResources{VCPUs: 1, MemoryGB: 1}
	}
	largest := nodeTypes[0]
	for _, nt := range nodeTypes[1:] {
		if nt.VCPUs > largest.VCPUs || (nt.VCPUs == largest.VCPUs && nt.MemoryGB > largest.MemoryGB) {
			largest = nt
		}
	}
	return NodeResources{VCPUs: largest.VCPUs, MemoryGB: largest.MemoryGB}
}

// largestInstanceType returns the instance type name of the largest configured node type.
func largestInstanceType(nodeTypes []config.NodeTypeConfig) string {
	r := largestNodeResources(nodeTypes)
	for _, nt := range nodeTypes {
		if nt.VCPUs == r.VCPUs && nt.MemoryGB == r.MemoryGB {
			return nt.InstanceType
		}
	}
	return ""
}

// Autoscaler monitors agent resource pressure and triggers scale events.
type Autoscaler struct {
	registry    *registry.Registry
	agentClient *client.AgentClient
	provider    CloudProvider
	cfg         *config.AutoscaleConfig

	mu              sync.Mutex
	pressureHistory map[string][]pressureSample

	lastScaleUp   time.Time
	lastScaleDown time.Time
}

type pressureSample struct {
	cpuPercent float64
	memPercent float64
	at         time.Time
}

// newAutoscaler creates an autoscaler for the given provider and config.
// Returns nil if autoscaling is not enabled. Internal — called by ProvidersManager.
func newAutoscaler(
	reg *registry.Registry,
	agentClient *client.AgentClient,
	provider CloudProvider,
	cfg *config.AutoscaleConfig,
) (*Autoscaler, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}
	return &Autoscaler{
		registry:        reg,
		agentClient:     agentClient,
		provider:        provider,
		cfg:             cfg,
		pressureHistory: make(map[string][]pressureSample),
	}, nil
}

// Start begins the monitoring loop in a background goroutine.
func (a *Autoscaler) Start(ctx context.Context) {
	pollInterval := time.Duration(a.cfg.PollIntervalSec) * time.Second

	log.Printf("[autoscaler] Started (provider=%s name=%s) — cpu_upper=%.0f%% mem_upper=%.0f%% cpu_lower=%.0f%% mem_lower=%.0f%% window=%ds cooldown=%ds max=%d min=%d poll=%ds",
		a.provider.Name(), a.cfg.Name,
		a.cfg.CPUUpperThreshold, a.cfg.MemUpperThreshold,
		a.cfg.CPULowerThreshold, a.cfg.MemLowerThreshold,
		a.cfg.EvaluationWindowSec, a.cfg.CooldownSec,
		a.cfg.MaxNodes, a.cfg.MinNodes, a.cfg.PollIntervalSec)

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

// poll fetches metrics from all online agents, probes offline agents for
// promotion, then evaluates whether to scale.
func (a *Autoscaler) poll(ctx context.Context) {
	now := time.Now()
	window := time.Duration(a.cfg.EvaluationWindowSec) * time.Second

		for _, agent := range a.registry.GetAllAgents() {
		if agent.Status == models.AgentDraining {
			continue
		}

		status, err := a.agentClient.GetNodeStatus(agent)
		if err != nil {
			if agent.Status == models.AgentOnline {
				log.Printf("[autoscaler] Agent %s unreachable, marking offline: %v", agent.ID, err)
				a.registry.SetOffline(agent.ID)
			}
			continue
		}

		a.registry.UpdateNodeStatus(agent.ID, status)

		a.mu.Lock()
		a.pressureHistory[agent.ID] = append(a.pressureHistory[agent.ID], pressureSample{
			cpuPercent: status.CPUPercent,
			memPercent: status.MemPercent,
			at:         now,
		})
		trimmed := a.pressureHistory[agent.ID][:0]
		for _, s := range a.pressureHistory[agent.ID] {
			if now.Sub(s.at) <= window {
				trimmed = append(trimmed, s)
			}
		}
		a.pressureHistory[agent.ID] = trimmed
		a.mu.Unlock()
	}

	a.checkAndTriggerScale(ctx)
}

// shouldScale evaluates sustained pressure across all online agents.
// High: every sample on every agent has CPU >= upper OR mem >= upper.
// Low:  every sample on every agent has CPU <= lower AND mem <= lower.
func (a *Autoscaler) shouldScale() ScaleDirection {
	a.mu.Lock()
	defer a.mu.Unlock()

	agents := a.registry.GetOnlineAgents()
	if len(agents) == 0 {
		return ScaleDirectionNeutral
	}

	now := time.Now()
	window := time.Duration(a.cfg.EvaluationWindowSec) * time.Second

	allHigh := true
	allLow := true

	for _, agent := range agents {
		history, ok := a.pressureHistory[agent.ID]
		if !ok || len(history) == 0 {
			return ScaleDirectionNeutral
		}

		// Require samples spanning the full window before deciding.
		if now.Sub(history[0].at) < window {
			return ScaleDirectionNeutral
		}

		agentHigh := true
		agentLow := true

		for _, s := range history {
			if s.cpuPercent < a.cfg.CPUUpperThreshold && s.memPercent < a.cfg.MemUpperThreshold {
				agentHigh = false
			}
			if s.cpuPercent > a.cfg.CPULowerThreshold || s.memPercent > a.cfg.MemLowerThreshold {
				agentLow = false
			}
		}

		if !agentHigh {
			allHigh = false
		}
		if !agentLow {
			allLow = false
		}
	}

	if allHigh {
		return ScaleDirectionUp
	}
	if allLow {
		return ScaleDirectionDown
	}
	return ScaleDirectionNeutral
}

// tryScaleUp attempts a scale-up, respecting cooldown and max-node limits.
// Used by both the regular poll cycle and demand-driven hints.
func (a *Autoscaler) tryScaleUp(ctx context.Context) {
	cooldown := time.Duration(a.cfg.CooldownSec) * time.Second
	if time.Since(a.lastScaleUp) < cooldown {
		log.Printf("[autoscaler] Scale-up skipped: cooldown active (%.0fs remaining)",
			(cooldown - time.Since(a.lastScaleUp)).Seconds())
		return
	}

	allAgents := a.registry.GetAllAgents()

	if len(allAgents) < a.cfg.MaxNodes {
		a.lastScaleUp = time.Now()
		a.spawnNode(ctx, smallestNodeResources(a.cfg.NodeTypes))
		return
	}

	// At max nodes — drain and replace an idle managed node with the largest type.
	maxType := largestInstanceType(a.cfg.NodeTypes)
	maxRes := largestNodeResources(a.cfg.NodeTypes)

	var candidate *models.Agent
	for _, ag := range allAgents {
		if ag.ProviderID != "" && ag.InstanceType != maxType && ag.ActiveCount == 0 {
			candidate = ag
			break
		}
	}
	if candidate == nil {
		log.Printf("[autoscaler] At max nodes (%d/%d), all managed nodes are max hardware or busy",
			len(allAgents), a.cfg.MaxNodes)
		return
	}

	log.Printf("[autoscaler] At max nodes — upgrading %s (%s → %s)",
		candidate.ID, candidate.InstanceType, maxType)

	a.lastScaleUp = time.Now()
	a.registry.SetDraining(candidate.ID)

	cid, cpid, caddr := candidate.ID, candidate.ProviderID, candidate.Address
	go func() {
		a.drainAndTerminate(ctx, cid, caddr, cpid)
		a.spawnNode(ctx, maxRes)
	}()
}

func (a *Autoscaler) checkAndTriggerScale(ctx context.Context) {
	dir := a.shouldScale()
	if dir == ScaleDirectionNeutral {
		return
	}

	cooldown := time.Duration(a.cfg.CooldownSec) * time.Second

	if dir == ScaleDirectionUp {
		a.logPressureSummary("up")
		a.tryScaleUp(ctx)
		return
	}

	// ScaleDirectionDown
	if time.Since(a.lastScaleDown) < cooldown {
		log.Printf("[autoscaler] Scale-down skipped: cooldown active (%.0fs remaining)",
			(cooldown - time.Since(a.lastScaleDown)).Seconds())
		return
	}

	allAgents := a.registry.GetAllAgents()

	if len(allAgents) <= a.cfg.MinNodes {
		log.Printf("[autoscaler] Scale-down skipped: already at min nodes (%d/%d)",
			len(allAgents), a.cfg.MinNodes)
		return
	}

	// Pick the least-pressured idle managed node to decommission.
	var target *models.Agent
	for _, ag := range allAgents {
		if ag.ProviderID == "" || ag.ActiveCount > 0 || ag.Status == models.AgentDraining {
			continue
		}
		if target == nil || ag.Pressure() < target.Pressure() {
			target = ag
		}
	}

	if target == nil {
		log.Printf("[autoscaler] Scale-down skipped: no idle managed nodes available")
		return
	}

	a.lastScaleDown = time.Now()
	a.logPressureSummary("down")

	a.registry.SetDraining(target.ID)

	tid, tpid, taddr := target.ID, target.ProviderID, target.Address
	go a.drainAndTerminate(ctx, tid, taddr, tpid)

	a.mu.Lock()
	a.pressureHistory = make(map[string][]pressureSample)
	a.mu.Unlock()
}

// drainAndTerminate waits for the agent to finish all active tasks then terminates it.
func (a *Autoscaler) drainAndTerminate(ctx context.Context, agentID, agentAddr, providerID string) {
	log.Printf("[autoscaler] Draining %s before termination...", agentID)

	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	probe := &models.Agent{Address: agentAddr}

loop:
	for {
		select {
		case <-drainCtx.Done():
			if ctx.Err() != nil {
				return // parent canceled, abort
			}
			log.Printf("[autoscaler] Drain timeout for %s, terminating anyway", agentID)
			break loop
		case <-ticker.C:
			status, err := a.agentClient.GetNodeStatus(probe)
			if err != nil {
				log.Printf("[autoscaler] Drain poll for %s: %v", agentID, err)
				continue
			}
			if status.ActiveTasks == 0 {
				log.Printf("[autoscaler] %s drained", agentID)
				break loop
			}
			log.Printf("[autoscaler] Waiting to drain %s (%d active tasks)", agentID, status.ActiveTasks)
		}
	}

	a.registry.DeregisterAgent(agentID)

	if err := a.provider.TerminateNode(ctx, providerID); err != nil {
		log.Printf("[autoscaler] Terminate %s failed: %v", agentID, err)
		return
	}
	log.Printf("[autoscaler] Terminated %s (nodes remaining: %d)", agentID, len(a.registry.GetAllAgents()))
}

// spawnNode provisions a new node, registers it as offline, and bootstraps it async.
func (a *Autoscaler) spawnNode(ctx context.Context, resources NodeResources) {
	a.logPressureSummary("up")

	node, err := a.provider.SpawnNode(ctx, resources)
	if err != nil {
		log.Printf("[autoscaler] Scale-up failed (spawn): %v", err)
		return
	}

	agentAddr := fmt.Sprintf("%s:%d", node.PrivateIP, config.Get().AgentPort)
	a.registry.RegisterOfflineAgent(node.AgentID, agentAddr, node.ProviderID, node.InstanceType)

	log.Printf("[autoscaler] Node spawned: agent=%s type=%s addr=%s — bootstrapping...",
		node.AgentID, node.InstanceType, agentAddr)

	go func() {
		if err := a.provider.Bootstrap(ctx, node); err != nil {
			log.Printf("[autoscaler] Bootstrap failed for %s: %v — terminating", node.AgentID, err)
			a.registry.DeregisterAgent(node.AgentID)
			if terr := a.provider.TerminateNode(ctx, node.ProviderID); terr != nil {
				log.Printf("[autoscaler] Failed to terminate %s after bootstrap failure: %v", node.ProviderID, terr)
			} else {
				log.Printf("[autoscaler] Terminated %s after bootstrap failure", node.ProviderID)
			}
			return
		}
		log.Printf("[autoscaler] Bootstrap complete: agent=%s", node.AgentID)
	}()

	a.mu.Lock()
	a.pressureHistory = make(map[string][]pressureSample)
	a.mu.Unlock()
}

// logPressureSummary logs the average pressure across online agents.
func (a *Autoscaler) logPressureSummary(direction string) {
	agents := a.registry.GetOnlineAgents()
	a.mu.Lock()
	var sum float64
	var count int
	for _, agent := range agents {
		if h, ok := a.pressureHistory[agent.ID]; ok && len(h) > 0 {
			last := h[len(h)-1]
			sum += last.cpuPercent + last.memPercent
			count++
		}
	}
	a.mu.Unlock()
	if count > 0 {
		sum /= float64(count)
	}
	log.Printf("[autoscaler] Scaling %s — avg pressure=%.1f%% across %d agents", direction, sum, len(agents))
}
