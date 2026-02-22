package scaler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"flux/pkg/client"
	"flux/pkg/config"
	"flux/pkg/registry"
)

// providerEntry pairs a cloud provider with the minimum fleet size and node
// types needed to initialise it.
type providerEntry struct {
	provider  CloudProvider
	minNodes  int
	nodeTypes []config.NodeTypeConfig
}

// ProvidersManager owns cloud providers and is responsible for bootstrapping
// the minimum node fleet. It creates and starts autoscalers but releases all
// references to them after Start — they manage themselves autonomously.
type ProvidersManager struct {
	reg     *registry.Registry
	entries []providerEntry
	scalers []*Autoscaler // held only until Start is called
	ctx     context.Context
}

// NewProvidersManager builds providers and autoscalers from the global config.
func NewProvidersManager(reg *registry.Registry, agentClient *client.AgentClient) (*ProvidersManager, error) {
	m := &ProvidersManager{reg: reg}

	providersCfg := config.Get().Providers
	if providersCfg == nil {
		return m, nil
	}

	if awsCfg := providersCfg.AWS; awsCfg != nil {
		provider, err := NewAWSProvider()
		if err != nil {
			return nil, fmt.Errorf("aws provider: %w", err)
		}

		entry := providerEntry{provider: provider}

		if as := awsCfg.Autoscaling; as != nil {
			entry.minNodes = as.MinNodes
			entry.nodeTypes = as.NodeTypes

			a, err := newAutoscaler(reg, agentClient, provider, as)
			if err != nil {
				return nil, fmt.Errorf("aws autoscaler: %w", err)
			}
			if a != nil {
				m.scalers = append(m.scalers, a)
			}
		}

		m.entries = append(m.entries, entry)
	}

	return m, nil
}

// Start stores the server lifetime context, launches all autoscalers, then
// releases references to them — they run autonomously with no external handles.
func (m *ProvidersManager) Start(ctx context.Context) {
	m.ctx = ctx
	for _, a := range m.scalers {
		a.Start(ctx)
	}
	m.scalers = nil
}

// InitializeNodes spawns enough nodes across all providers to reach each
// provider's configured minimum. Waits for all spawns to complete.
// Returns (succeeded, attempted) — caller should treat succeeded < attempted as a failure.
func (m *ProvidersManager) InitializeNodes() (int, int) {
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	agentPort := config.Get().AgentPort
	var wg sync.WaitGroup
	var succeeded, attempted atomic.Int32

	for _, e := range m.entries {
		allAgents := m.reg.GetAllAgents()
		need := e.minNodes - len(allAgents)
		if need <= 0 {
			log.Printf("[providers] Already at or above min nodes (%d/%d)", len(allAgents), e.minNodes)
			continue
		}
		if len(e.nodeTypes) == 0 {
			log.Printf("[providers] No node_types configured, skipping")
			continue
		}
		res := smallestNodeResources(e.nodeTypes)
		log.Printf("[providers] Spawning %d node(s) to reach min=%d", need, e.minNodes)

		for i := 0; i < need; i++ {
			wg.Add(1)
			attempted.Add(1)
			go func() {
				defer wg.Done()
				node, err := e.provider.SpawnNode(ctx, res)
				if err != nil {
					log.Printf("[providers] Spawn failed: %v", err)
					return
				}
				agentAddr := fmt.Sprintf("%s:%d", node.PrivateIP, agentPort)
				m.reg.RegisterOfflineAgent(node.AgentID, agentAddr, node.ProviderID, node.InstanceType)
				log.Printf("[providers] Node spawned: agent=%s type=%s addr=%s — bootstrapping...",
					node.AgentID, node.InstanceType, agentAddr)
				if err := e.provider.Bootstrap(ctx, node); err != nil {
					log.Printf("[providers] Bootstrap failed for %s: %v — terminating", node.AgentID, err)
					m.reg.DeregisterAgent(node.AgentID)
					if terr := e.provider.TerminateNode(ctx, node.ProviderID); terr != nil {
						log.Printf("[providers] Failed to terminate %s after bootstrap failure: %v", node.ProviderID, terr)
					} else {
						log.Printf("[providers] Terminated %s after bootstrap failure", node.ProviderID)
					}
					return
				}
				log.Printf("[providers] Bootstrap complete: agent=%s", node.AgentID)
				succeeded.Add(1)
			}()
		}
	}

	wg.Wait()
	return int(succeeded.Load()), int(attempted.Load())
}
