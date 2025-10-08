package knowledgegraph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"go.uber.org/zap"
)

// InMemoryKG provides a fast, ephemeral, in-memory implementation of the KnowledgeGraph interface.
// It's great for testing, short lived scripts, or situations where persistence isn't required.
type InMemoryKG struct {
	nodes         map[string]schemas.Node
	edges         map[string]schemas.Edge // Key: edge ID
	outgoingEdges map[string][]string     // Key: node ID, Value: slice of edge IDs
	mu            sync.RWMutex
	log           *zap.Logger
}

// Ensures InMemoryKG correctly implements the KnowledgeGraphClient interface at compile time.
var _ schemas.KnowledgeGraphClient = (*InMemoryKG)(nil)

// NewInMemoryKG creates a new, empty in-memory knowledge graph.
func NewInMemoryKG(logger *zap.Logger) (*InMemoryKG, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &InMemoryKG{
		nodes:         make(map[string]schemas.Node),
		edges:         make(map[string]schemas.Edge),
		outgoingEdges: make(map[string][]string),
		log:           logger.Named("InMemoryKG"),
	}, nil
}

// AddNode adds a node to the graph. If a node with the same ID already exists, it is overwritten.
func (kg *InMemoryKG) AddNode(ctx context.Context, node schemas.Node) error {
	kg.mu.Lock()
	defer kg.mu.Unlock()

	kg.nodes[node.ID] = node
	kg.log.Debug("Node added or updated", zap.String("ID", node.ID), zap.String("Type", string(node.Type)))
	return nil
}

// AddEdge adds an edge to the graph. If an edge with the same ID already exists, it's overwritten.
func (kg *InMemoryKG) AddEdge(ctx context.Context, edge schemas.Edge) error {
	kg.mu.Lock()
	defer kg.mu.Unlock()

	if _, exists := kg.nodes[edge.From]; !exists {
		return fmt.Errorf("source node with id '%s' not found for edge", edge.From)
	}
	if _, exists := kg.nodes[edge.To]; !exists {
		return fmt.Errorf("destination node with id '%s' not found for edge", edge.To)
	}

	isNew := true
	if _, exists := kg.edges[edge.ID]; exists {
		isNew = false
	}

	kg.edges[edge.ID] = edge

	if isNew {
		kg.outgoingEdges[edge.From] = append(kg.outgoingEdges[edge.From], edge.ID)
	}

	kg.log.Debug("Edge added or updated", zap.String("ID", edge.ID), zap.String("From", edge.From), zap.String("To", edge.To))
	return nil
}

// GetNode retrieves a node by its ID.
func (kg *InMemoryKG) GetNode(ctx context.Context, id string) (schemas.Node, error) {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	node, ok := kg.nodes[id]
	if !ok {
		return schemas.Node{}, fmt.Errorf("node with id '%s' not found", id)
	}
	return node, nil
}

// GetEdge retrieves an edge by its ID.
func (kg *InMemoryKG) GetEdge(ctx context.Context, id string) (schemas.Edge, error) {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	edge, ok := kg.edges[id]
	if !ok {
		return schemas.Edge{}, fmt.Errorf("edge with id '%s' not found", id)
	}
	return edge, nil
}

// GetNeighbors finds all nodes connected from the given node.
func (kg *InMemoryKG) GetNeighbors(ctx context.Context, nodeID string) ([]schemas.Node, error) {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	if _, ok := kg.nodes[nodeID]; !ok {
		return nil, fmt.Errorf("node with id '%s' not found", nodeID)
	}

	edgeIDs, ok := kg.outgoingEdges[nodeID]
	if !ok {
		return []schemas.Node{}, nil // No outgoing edges.
	}

	neighbors := make([]schemas.Node, 0, len(edgeIDs))
	for _, edgeID := range edgeIDs {
		edge := kg.edges[edgeID]
		neighborNode := kg.nodes[edge.To]
		neighbors = append(neighbors, neighborNode)
	}
	return neighbors, nil
}

// GetEdges retrieves all outgoing edges from a specific node ID.
func (kg *InMemoryKG) GetEdges(ctx context.Context, nodeID string) ([]schemas.Edge, error) {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	if _, ok := kg.nodes[nodeID]; !ok {
		return nil, fmt.Errorf("node with id '%s' not found", nodeID)
	}

	edgeIDs, ok := kg.outgoingEdges[nodeID]
	if !ok {
		return []schemas.Edge{}, nil // No outgoing edges.
	}

	edges := make([]schemas.Edge, 0, len(edgeIDs))
	for _, edgeID := range edgeIDs {
		edge := kg.edges[edgeID]
		edges = append(edges, edge)
	}

	return edges, nil
}

// QueryImprovementHistory finds past improvement attempts related to the current goal objective.
// This provides the "memory" for the Reflective OODA loop.
func (kg *InMemoryKG) QueryImprovementHistory(ctx context.Context, goalObjective string, limit int) ([]schemas.Node, error) {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	var matchingNodes []schemas.Node

	// This in-memory version requires a full scan and unmarshalling to filter.
	for _, node := range kg.nodes {
		if node.Type == schemas.NodeImprovementAttempt {
			var props schemas.ImprovementAttemptProperties
			if err := json.Unmarshal(node.Properties, &props); err != nil {
				kg.log.Warn("Failed to unmarshal properties for history query", zap.String("node_id", node.ID), zap.Error(err))
				continue
			}

			// A simple similarity check (case-insensitive).
			if strings.EqualFold(props.GoalObjective, goalObjective) {
				matchingNodes = append(matchingNodes, node)
			}
		}
	}

	// Sort by CreatedAt descending to get the most recent attempts first.
	sort.Slice(matchingNodes, func(i, j int) bool {
		return matchingNodes[i].CreatedAt.After(matchingNodes[j].CreatedAt)
	})

	if limit > 0 && len(matchingNodes) > limit {
		return matchingNodes[:limit], nil
	}

	return matchingNodes, nil
}
