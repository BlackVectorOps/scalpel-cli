package knowledgegraph

import (
	"context"
	"errors" // REVISION: Added for sentinel errors.
	"fmt"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/pkg/graphmodel"
	"github.com/xkilldash9x/scalpel-cli/pkg/interfaces"
	"github.com/xkilldash9x/scalpel-cli/pkg/observability"
)

// REVISION: Added a sentinel error for not found resources.
var ErrNotFound = errors.New("resource not found")

// -- Internal data structures --

// Set is a simple way to store unique node ids.
type Set map[string]struct{}

func (s Set) Add(item string)      { s[item] = struct{}{} }
func (s Set) Remove(item string)   { delete(s, item) }
func (s Set) Contains(item string) bool { _, exists := s[item]; return exists }
func (s Set) Size() int            { return len(s) }

// PropertyIndex is the main index for searching nodes by their properties.
// Structure: map[PropertyKey] -> map[PropertyValue] -> Set[NodeIDs]
type PropertyIndex map[string]map[interface{}]Set

// EdgeMap is the optimized structure for O(1) edge lookups.
// Structure: map[AdjacentNodeID] -> map[RelationshipType] -> *Edge
type EdgeMap map[string]map[graphmodel.RelationshipType]*graphmodel.Edge

// Indexes bundles up all indexes for fast lookups.
type Indexes struct {
	ByType        map[graphmodel.NodeType]Set
	ByProperty    map[graphmodel.NodeType]PropertyIndex
	OutboundEdges map[string]EdgeMap // map[SourceID] -> EdgeMap
	InboundEdges  map[string]EdgeMap // map[TargetID] -> EdgeMap
}

// KnowledgeGraph is the main, thread safe in memory graph database.
// It implements the KnowledgeGraph interface.
type KnowledgeGraph struct {
	nodes   map[string]*graphmodel.Node
	indexes Indexes
	logger  *zap.Logger
	mutex   sync.RWMutex
}

// Ensure KnowledgeGraph implements the interfaces.KnowledgeGraph interface.
var _ interfaces.KnowledgeGraph = (*KnowledgeGraph)(nil)

// -- Initialization --

// New initializes a new KnowledgeGraph.
func New(logger *zap.Logger) *KnowledgeGraph {
	if logger == nil {
		logger = observability.NewNopLogger()
	}

	kg := &KnowledgeGraph{
		nodes: make(map[string]*graphmodel.Node),
		indexes: Indexes{
			ByType:        make(map[graphmodel.NodeType]Set),
			ByProperty:    make(map[graphmodel.NodeType]PropertyIndex),
			OutboundEdges: make(map[string]EdgeMap),
			InboundEdges:  make(map[string]EdgeMap),
		},
		logger: logger.Named("knowledge_graph"),
	}

	kg.initializeRootNodes()
	kg.logger.Info("KnowledgeGraph initialized.")
	return kg
}

// initializeRootNodes puts essential nodes in the graph.
func (kg *KnowledgeGraph) initializeRootNodes() {
	// No lock needed during initialization.
	if _, err := kg.addNodeInternal(graphmodel.NodeInput{
		ID:         graphmodel.RootNodeID,
		Type:       graphmodel.NodeTypeScanRoot,
		Properties: graphmodel.Properties{"description": "The origin of the scan."},
	}); err != nil {
		// REVISION: Fail fast if critical nodes cannot be created.
		kg.logger.Fatal("Critical failure: Failed to initialize Root Node", zap.Error(err))
	}

	if _, err := kg.addNodeInternal(graphmodel.NodeInput{
		ID:         graphmodel.OSINTNodeID,
		Type:       graphmodel.NodeTypeDataSource,
		Properties: graphmodel.Properties{"description": "Data derived from open source intelligence."},
	}); err != nil {
		// REVISION: Fail fast if critical nodes cannot be created.
		kg.logger.Fatal("Critical failure: Failed to initialize OSINT Node", zap.Error(err))
	}
}

// -- Public Write Operations --

// AddNode adds or updates a node in the graph (Upsert).
func (kg *KnowledgeGraph) AddNode(input graphmodel.NodeInput) (*graphmodel.Node, error) {
	if input.ID == "" {
		return nil, fmt.Errorf("node ID cannot be empty")
	}

	// If type is provided, we use it. If not, we infer it only if it's a new node.
	// For existing nodes, the type is immutable, so we don't infer/change it implicitly.
	kg.mutex.Lock()
	defer kg.mutex.Unlock()

	// REVISION: Return a clone to prevent data races.
	node, err := kg.addNodeInternal(input)
	if err != nil {
		return nil, err
	}
	return node.Clone(), nil
}

// AddEdge adds or updates a relationship between two nodes (Upsert).
func (kg *KnowledgeGraph) AddEdge(input graphmodel.EdgeInput) (*graphmodel.Edge, error) {
	if input.SourceID == "" || input.TargetID == "" || input.Relationship == "" {
		return nil, fmt.Errorf("SourceID, TargetID, and Relationship cannot be empty")
	}

	// Prevent self referential edges unless explicitly allowed (e.g., NEXT_ACTION for sequences).
	if input.SourceID == input.TargetID && input.Relationship != graphmodel.RelationshipTypeNextAction {
		kg.logger.Debug("Attempted to add a self-referential edge, ignoring.",
			zap.String("id", input.SourceID),
			zap.String("rel", string(input.Relationship)))
		return nil, nil // Not an error, just an ignored operation.
	}

	kg.mutex.Lock()
	defer kg.mutex.Unlock()

	// REVISION: Return a clone to prevent data races.
	edge, err := kg.addEdgeInternal(input)
	if err != nil {
		return nil, err
	}
	return edge.Clone(), nil
}

// -- Internal Write Operations (must be called from within a mutex lock) --

func (kg *KnowledgeGraph) addNodeInternal(input graphmodel.NodeInput) (*graphmodel.Node, error) {
	// ... (implementation is unchanged) ...
	// This internal function still returns a direct pointer, but the public-facing
	// methods now clone the result before returning.
	now := time.Now().UTC()
	id := input.ID

	if existingNode, exists := kg.nodes[id]; exists {
		if input.Type != "" && existingNode.Type != input.Type {
			err := fmt.Errorf("cannot change type of existing node '%s' from '%s' to '%s'", id, existingNode.Type, input.Type)
			kg.logger.Error("Node update aborted due to type change attempt", zap.String("id", id), zap.Error(err))
			return nil, err
		}
		kg.removePropertyIndexes(existingNode)
		if existingNode.Properties == nil {
			existingNode.Properties = make(graphmodel.Properties)
		}
		for k, v := range input.Properties {
			existingNode.Properties[k] = v
		}
		existingNode.UpdatedAt = now
		kg.updateIndexes(existingNode)
		kg.logger.Debug("Updated node", zap.String("id", id))
		return existingNode, nil
	}

	nodeType := input.Type
	if nodeType == "" {
		nodeType = InferAssetType(id)
	}
	newNode := &graphmodel.Node{
		ID:         id,
		Type:       nodeType,
		Properties: input.Properties,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if newNode.Properties == nil {
		newNode.Properties = make(graphmodel.Properties)
	}
	kg.nodes[id] = newNode
	kg.indexes.OutboundEdges[id] = make(EdgeMap)
	kg.indexes.InboundEdges[id] = make(EdgeMap)
	kg.updateIndexes(newNode)
	kg.logger.Debug("Added node", zap.String("id", id), zap.String("type", string(newNode.Type)))
	return newNode, nil
}

func (kg *KnowledgeGraph) addEdgeInternal(input graphmodel.EdgeInput) (*graphmodel.Edge, error) {
	// ... (implementation is unchanged) ...
	if _, exists := kg.nodes[input.SourceID]; !exists {
		return nil, fmt.Errorf("source node not found: %s", input.SourceID)
	}
	if _, exists := kg.nodes[input.TargetID]; !exists {
		return nil, fmt.Errorf("target node not found: %s", input.TargetID)
	}
	var existingEdge *graphmodel.Edge
	var edgeExists bool
	outboundMap := kg.indexes.OutboundEdges[input.SourceID]
	if relationshipMap, ok := outboundMap[input.TargetID]; ok {
		existingEdge, edgeExists = relationshipMap[input.Relationship]
	}
	now := time.Now().UTC()
	if edgeExists {
		if existingEdge.Properties == nil {
			existingEdge.Properties = make(graphmodel.Properties)
		}
		for k, v := range input.Properties {
			existingEdge.Properties[k] = v
		}
		existingEdge.Timestamp = now
		kg.logger.Debug("Updated edge", zap.String("source", input.SourceID), zap.String("rel", string(input.Relationship)), zap.String("target", input.TargetID))
		return existingEdge, nil
	}
	newEdge := &graphmodel.Edge{
		SourceID:     input.SourceID,
		TargetID:     input.TargetID,
		Relationship: input.Relationship,
		Properties:   input.Properties,
		Timestamp:    now,
	}
	if newEdge.Properties == nil {
		newEdge.Properties = make(graphmodel.Properties)
	}
	if outboundMap[input.TargetID] == nil {
		outboundMap[input.TargetID] = make(map[graphmodel.RelationshipType]*graphmodel.Edge)
	}
	outboundMap[input.TargetID][input.Relationship] = newEdge
	inboundMap := kg.indexes.InboundEdges[input.TargetID]
	if inboundMap[input.SourceID] == nil {
		inboundMap[input.SourceID] = make(map[graphmodel.RelationshipType]*graphmodel.Edge)
	}
	inboundMap[input.SourceID][input.Relationship] = newEdge
	kg.logger.Debug("Added edge", zap.String("source", input.SourceID), zap.String("rel", string(input.Relationship)), zap.String("target", input.TargetID))
	return newEdge, nil
}

// -- Index Management (must be called from within a mutex lock) --

func (kg *KnowledgeGraph) updateIndexes(node *graphmodel.Node) {
	// ... (implementation is unchanged) ...
	if _, exists := kg.indexes.ByType[node.Type]; !exists {
		kg.indexes.ByType[node.Type] = make(Set)
	}
	kg.indexes.ByType[node.Type].Add(node.ID)
	if _, exists := kg.indexes.ByProperty[node.Type]; !exists {
		kg.indexes.ByProperty[node.Type] = make(PropertyIndex)
	}
	typeIndex := kg.indexes.ByProperty[node.Type]
	for key, value := range node.Properties {
		if !isComparable(value) {
			kg.logger.Warn("Skipping index for non-comparable property value", zap.String("id", node.ID), zap.String("key", key), zap.String("type", fmt.Sprintf("%T", value)))
			continue
		}
		if _, exists := typeIndex[key]; !exists {
			typeIndex[key] = make(map[interface{}]Set)
		}
		propertyIndex := typeIndex[key]
		if _, exists := propertyIndex[value]; !exists {
			propertyIndex[value] = make(Set)
		}
		propertyIndex[value].Add(node.ID)
	}
}

func (kg *KnowledgeGraph) removePropertyIndexes(node *graphmodel.Node) {
	typeIndex, exists := kg.indexes.ByProperty[node.Type]
	if !exists {
		return
	}
	for key, value := range node.Properties {
		if !isComparable(value) {
			continue
		}
		if propertyIndex, exists := typeIndex[key]; exists {
			if valueSet, exists := propertyIndex[value]; exists {
				valueSet.Remove(node.ID)
				if valueSet.Size() == 0 {
					delete(propertyIndex, value)
				}
			}
			if len(propertyIndex) == 0 {
				delete(typeIndex, key)
			}
		}
	}
	// REVISION: Added final cleanup of the top-level NodeType index.
	if len(typeIndex) == 0 {
		delete(kg.indexes.ByProperty, node.Type)
	}
}

// -- Public Read Operations --

// GetNodeByID retrieves a node by its unique identifier.
func (kg *KnowledgeGraph) GetNodeByID(id string) (*graphmodel.Node, error) {
	kg.mutex.RLock()
	defer kg.mutex.RUnlock()
	node, exists := kg.nodes[id]
	if !exists {
		// REVISION: Use the defined sentinel error.
		return nil, ErrNotFound
	}
	// REVISION: Return a clone to prevent data races.
	return node.Clone(), nil
}

// FindNodes retrieves nodes using indexes based on a query.
func (kg *KnowledgeGraph) FindNodes(query graphmodel.Query) ([]*graphmodel.Node, error) {
	kg.mutex.RLock()
	defer kg.mutex.RUnlock()

	// The logic for finding candidate IDs remains the same.
	// The change is in idsToNodes, which now returns clones.
	if query.Type == "" && len(query.Properties) == 0 {
		// REVISION: Return clones for "get all" queries.
		results := make([]*graphmodel.Node, 0, len(kg.nodes))
		for _, node := range kg.nodes {
			results = append(results, node.Clone())
		}
		return results, nil
	}

	var candidateIds Set
	if query.Type != "" {
		if ids, exists := kg.indexes.ByType[query.Type]; exists {
			candidateIds = make(Set, len(ids))
			for id := range ids {
				candidateIds.Add(id)
			}
		} else {
			return []*graphmodel.Node{}, nil
		}
	}
	if len(query.Properties) == 0 {
		return kg.idsToNodes(candidateIds), nil
	}
	if query.Type != "" {
		typePropIndex, exists := kg.indexes.ByProperty[query.Type]
		if !exists {
			return []*graphmodel.Node{}, nil
		}
		for key, value := range query.Properties {
			if !isComparable(value) {
				return nil, fmt.Errorf("query value for key '%s' is not comparable", key)
			}
			propertyIndex, exists := typePropIndex[key]
			if !exists {
				return []*graphmodel.Node{}, nil
			}
			matchingIds, exists := propertyIndex[value]
			if !exists {
				return []*graphmodel.Node{}, nil
			}
			candidateIds = intersectSets(candidateIds, matchingIds)
			if candidateIds.Size() == 0 {
				return []*graphmodel.Node{}, nil
			}
		}
	} else {
		kg.logger.Warn("Querying by property without a 'Type' can be inefficient.")
		isFirstProperty := true
		for key, value := range query.Properties {
			if !isComparable(value) {
				return nil, fmt.Errorf("query value for key '%s' is not comparable", key)
			}
			matchingIdsForProp := make(Set)
			for _, typePropIndex := range kg.indexes.ByProperty {
				if propertyIndex, exists := typePropIndex[key]; exists {
					if ids, exists := propertyIndex[value]; exists {
						for id := range ids {
							matchingIdsForProp.Add(id)
						}
					}
				}
			}
			if isFirstProperty {
				candidateIds = matchingIdsForProp
				isFirstProperty = false
			} else {
				candidateIds = intersectSets(candidateIds, matchingIdsForProp)
			}
			if candidateIds == nil || candidateIds.Size() == 0 {
				return []*graphmodel.Node{}, nil
			}
		}
	}
	return kg.idsToNodes(candidateIds), nil
}

// GetNeighbors retrieves all nodes directly connected to the given node.
func (kg *KnowledgeGraph) GetNeighbors(nodeId string) (graphmodel.NeighborsResult, error) {
	kg.mutex.RLock()
	defer kg.mutex.RUnlock()

	if _, exists := kg.nodes[nodeId]; !exists {
		// REVISION: Use the defined sentinel error.
		return graphmodel.NeighborsResult{}, ErrNotFound
	}

	result := graphmodel.NeighborsResult{
		Outbound: make(map[graphmodel.RelationshipType][]*graphmodel.Node),
		Inbound:  make(map[graphmodel.RelationshipType][]*graphmodel.Node),
	}

	if outboundMap, ok := kg.indexes.OutboundEdges[nodeId]; ok {
		for targetID, relationshipMap := range outboundMap {
			if targetNode, exists := kg.nodes[targetID]; exists {
				for rel := range relationshipMap {
					// REVISION: Append a clone of the node.
					result.Outbound[rel] = append(result.Outbound[rel], targetNode.Clone())
				}
			}
		}
	}

	if inboundMap, ok := kg.indexes.InboundEdges[nodeId]; ok {
		for sourceID, relationshipMap := range inboundMap {
			if sourceNode, exists := kg.nodes[sourceID]; exists {
				for rel := range relationshipMap {
					// REVISION: Append a clone of the node.
					result.Inbound[rel] = append(result.Inbound[rel], sourceNode.Clone())
				}
			}
		}
	}
	return result, nil
}

// -- Atomic Operations --

// RecordTechnology atomically adds a technology node and links it to an asset.
func (kg *KnowledgeGraph) RecordTechnology(assetId string, technologyName string, version string, source string, confidence float64, assetType graphmodel.NodeType) error {
	if assetId == "" || technologyName == "" {
		return fmt.Errorf("AssetID and TechnologyName cannot be empty")
	}

	safeVersion := version
	if safeVersion == "" {
		safeVersion = "unknown"
	}
	// ENHANCEMENT: Prefixing Tech IDs for namespacing and consistent inference.
	techId := fmt.Sprintf("TECH:%s:%s", technologyName, safeVersion)

	kg.mutex.Lock()
	defer kg.mutex.Unlock()

	// Ensure the asset node exists.
	if _, exists := kg.nodes[assetId]; !exists {
		nodeType := assetType
		if nodeType == "" {
			nodeType = InferAssetType(assetId)
		}
		if _, err := kg.addNodeInternal(graphmodel.NodeInput{ID: assetId, Type: nodeType}); err != nil {
			return fmt.Errorf("failed to create asset node %s: %w", assetId, err)
		}
	}

	// Upsert the technology node.
	if _, err := kg.addNodeInternal(graphmodel.NodeInput{
		ID:         techId,
		Type:       graphmodel.NodeTypeTechnology,
		Properties: graphmodel.Properties{"name": technologyName, "version": safeVersion},
	}); err != nil {
		kg.logger.Warn("Failed to upsert technology node, proceeding.", zap.Error(err))
	}

	// Upsert the relationship.
	_, err := kg.addEdgeInternal(graphmodel.EdgeInput{
		SourceID:     assetId,
		TargetID:     techId,
		Relationship: graphmodel.RelationshipTypeUsesTechnology,
		Properties:   graphmodel.Properties{"source": source, "confidence": confidence},
	})
	return err
}

// RecordLink atomically adds two nodes and the link between them.
func (kg *KnowledgeGraph) RecordLink(sourceUrl string, targetUrl string, method string, depth int) error {
	if targetUrl == "" {
		return fmt.Errorf("TargetURL cannot be empty")
	}

	sourceId := sourceUrl
	if sourceId == "" {
		sourceId = graphmodel.RootNodeID
	}
	if method == "" {
		method = "GET"
	}
	method = strings.ToUpper(method)

	kg.mutex.Lock()
	defer kg.mutex.Unlock()

	// Ensure source node exists (if not a system root)
	if sourceId != graphmodel.RootNodeID && sourceId != graphmodel.OSINTNodeID {
		if _, exists := kg.nodes[sourceId]; !exists {
			sourceDepth := 0
			if depth > 0 {
				sourceDepth = depth - 1
			}

			sourceType := InferAssetType(sourceId)
			if sourceType != graphmodel.NodeTypeURL {
				// Log if the source isn't a URL, but allow linking from other asset types.
				kg.logger.Debug("Source ID in RecordLink is not inferred as a URL.", zap.String("sourceId", sourceId))
			}

			if _, err := kg.addNodeInternal(graphmodel.NodeInput{
				ID:         sourceId,
				Type:       sourceType,
				Properties: graphmodel.Properties{"depth": sourceDepth},
			}); err != nil {
				return fmt.Errorf("failed to ensure source node %s: %w", sourceId, err)
			}
		}
	}

	// Ensure target node exists
	targetType := InferAssetType(targetUrl)
	if targetType != graphmodel.NodeTypeURL {
		// Log if the target isn't a URL, but allow linking to other asset types (e.g., binaries).
		kg.logger.Debug("Target ID in RecordLink is not inferred as a URL.", zap.String("targetId", targetUrl))
	}

	if _, err := kg.addNodeInternal(graphmodel.NodeInput{
		ID:         targetUrl,
		Type:       targetType,
		Properties: graphmodel.Properties{"depth": depth},
	}); err != nil {
		return fmt.Errorf("failed to ensure target node %s: %w", targetUrl, err)
	}

	_, err := kg.addEdgeInternal(graphmodel.EdgeInput{
		SourceID:     sourceId,
		TargetID:     targetUrl,
		Relationship: graphmodel.RelationshipTypeLinksTo,
		Properties:   graphmodel.Properties{"method": method},
	})
	return err
}

// ExportGraph dumps the entire graph into a serializable structure.
func (kg *KnowledgeGraph) ExportGraph() graphmodel.GraphExport {
	kg.mutex.RLock()
	defer kg.mutex.RUnlock()

	export := graphmodel.GraphExport{
		Nodes: make([]*graphmodel.Node, 0, len(kg.nodes)),
		Edges: make([]*graphmodel.Edge, 0),
	}
	// REVISION: Return clones for "get all" queries.
	for _, node := range kg.nodes {
		export.Nodes = append(export.Nodes, node.Clone())
	}

	// Iterate through OutboundEdges to export every edge exactly once.
	for _, targetMap := range kg.indexes.OutboundEdges {
		for _, relationshipMap := range targetMap {
			for _, edge := range relationshipMap {
				// REVISION: Append a clone of the edge.
				export.Edges = append(export.Edges, edge.Clone())
			}
		}
	}
	return export
}

// -- Contextualization (Localized Subgraph Extraction) --

// ExtractMissionSubgraph retrieves a localized subgraph relevant to the current mission.
// This is critical for managing the LLM context window.
func (kg *KnowledgeGraph) ExtractMissionSubgraph(ctx context.Context, missionID string, lookbackSteps int) (graphmodel.GraphExport, error) {
	kg.mutex.RLock()
	defer kg.mutex.RUnlock()

	// 1. Validate Mission Node
	_, exists := kg.nodes[missionID]
	if !exists {
		return graphmodel.GraphExport{}, ErrNotFound
	}

	includedNodeIDs := make(Set)
	export := graphmodel.GraphExport{
		Nodes: make([]*graphmodel.Node, 0),
		Edges: make([]*graphmodel.Edge, 0),
	}

	// 2. Include the Mission Node itself.
	includedNodeIDs.Add(missionID)

	// 3. Identify Recent History (Actions and Observations)
	actions := kg.findMissionActions(missionID)

	sort.Slice(actions, func(i, j int) bool {
		return actions[i].CreatedAt.Before(actions[j].CreatedAt)
	})

	recentHistoryIDs := make(Set)
	startIndex := 0
	if lookbackSteps > 0 && len(actions) > lookbackSteps {
		startIndex = len(actions) - lookbackSteps
	}

	// Iterate over the recent actions.
	for i := startIndex; i < len(actions); i++ {
		// REVISION: Check for context cancellation during potentially long loops.
		select {
		case <-ctx.Done():
			kg.logger.Warn("Subgraph extraction cancelled by context", zap.Error(ctx.Err()))
			return graphmodel.GraphExport{}, ctx.Err()
		default:
			// Continue processing
		}

		actionNode := actions[i]
		recentHistoryIDs.Add(actionNode.ID)
		if outboundMap, ok := kg.indexes.OutboundEdges[actionNode.ID]; ok {
			for relatedID, relMap := range outboundMap {
				if _, ok := relMap[graphmodel.RelationshipTypeGeneratesObservation]; ok {
					recentHistoryIDs.Add(relatedID)
				}
				if _, ok := relMap[graphmodel.RelationshipTypeGeneratesArtifact]; ok {
					recentHistoryIDs.Add(relatedID)
				}
			}
		}
	}

	for id := range recentHistoryIDs {
		includedNodeIDs.Add(id)
	}

	if outboundMap, ok := kg.indexes.OutboundEdges[missionID]; ok {
		for assetID, relMap := range outboundMap {
			if _, ok := relMap[graphmodel.RelationshipTypeAffects]; ok {
				if _, exists := kg.nodes[assetID]; exists {
					includedNodeIDs.Add(assetID)
					kg.includeAssetNeighbors(assetID, includedNodeIDs)
				}
			}
		}
	}

	// 5. Populate Nodes and Edges for the export.
	for id := range includedNodeIDs {
		if node, ok := kg.nodes[id]; ok {
			// REVISION: Append a clone of the node.
			export.Nodes = append(export.Nodes, node.Clone())
		}
	}
	// REVISION: Pass context to the helper for cancellation checking.
	kg.populateEdgesForSubgraph(ctx, &export, includedNodeIDs)

	return export, nil
}

// populateEdgesForSubgraph iterates through the included nodes and adds edges that connect them. (Assumes RLock is held).
func (kg *KnowledgeGraph) populateEdgesForSubgraph(ctx context.Context, export *graphmodel.GraphExport, includedNodeIDs Set) {
	addedEdgeSet := make(map[string]struct{})
	for sourceID := range includedNodeIDs {
		// REVISION: Check for context cancellation inside the loop.
		select {
		case <-ctx.Done():
			return // Exit early
		default:
		}

		if outboundMap, ok := kg.indexes.OutboundEdges[sourceID]; ok {
			for targetID, relationshipMap := range outboundMap {
				// Only include the edge if the target node is also in the subgraph.
				if includedNodeIDs.Contains(targetID) {
					for _, edge := range relationshipMap {
						// Create a unique key for the edge to check for duplicates.
						edgeKey := fmt.Sprintf("%s|%s|%s", edge.SourceID, edge.Relationship, edge.TargetID)
						if _, exists := addedEdgeSet[edgeKey]; !exists {
							// REVISION: Append a clone of the edge.
							export.Edges = append(export.Edges, edge.Clone())
							addedEdgeSet[edgeKey] = struct{}{}
						}
					}
				}
			}
		}
	}
}

// findMissionActions retrieves all action nodes connected to the mission. (Assumes RLock is held).
func (kg *KnowledgeGraph) findMissionActions(missionID string) []*graphmodel.Node {
	actions := make([]*graphmodel.Node, 0)
	if outboundMap, ok := kg.indexes.OutboundEdges[missionID]; ok {
		for actionID, relMap := range outboundMap {
			if _, ok := relMap[graphmodel.RelationshipTypeExecutesAction]; ok {
				if actionNode, ok := kg.nodes[actionID]; ok {
					actions = append(actions, actionNode)
				}
			}
		}
	}
	return actions
}

// includeAssetNeighbors adds immediate, relevant neighbors of an asset to the subgraph. (Assumes RLock is held).
func (kg *KnowledgeGraph) includeAssetNeighbors(assetID string, includedNodeIDs Set) {
	// Define relationships that provide valuable context for an asset.
	relevantRelationships := map[graphmodel.RelationshipType]bool{
		graphmodel.RelationshipTypeUsesTechnology: true,
		graphmodel.RelationshipTypeLinksTo:        true,
		graphmodel.RelationshipTypeHasParameter:   true,
	}

	if outboundMap, ok := kg.indexes.OutboundEdges[assetID]; ok {
		for neighborID, relMap := range outboundMap {
			isRelevant := false
			for rel := range relMap {
				if relevantRelationships[rel] {
					isRelevant = true
					break
				}
			}
			if isRelevant {
				if _, exists := kg.nodes[neighborID]; exists {
					includedNodeIDs.Add(neighborID)
				}
			}
		}
	}
}

// -- Utility Functions --

// Basic regex for validating domain names (simplified).
var domainRegex = regexp.MustCompile(`^([a-zA-Z0-9-]{1,63}\.)+[a-zA-Z]{2,63}$`)

// InferAssetType determines the appropriate NodeType for an identifier.
// Implemented as both a package level function (for external utility) and a method (to satisfy KnowledgeGraph interface).
func InferAssetType(assetId string) graphmodel.NodeType {
	// 1. Check for URL
	if u, err := url.Parse(assetId); err == nil && u.Scheme != "" && u.Host != "" {
		scheme := strings.ToLower(u.Scheme)
		if scheme == "http" || scheme == "https" || scheme == "ws" || scheme == "wss" {
			return graphmodel.NodeTypeURL
		}
	}
	// 2. Check for IP Address
	if net.ParseIP(assetId) != nil {
		return graphmodel.NodeTypeIPAddress
	}

	// 3. Check for Internal Prefixes
	if strings.HasPrefix(assetId, "TECH:") {
		return graphmodel.NodeTypeTechnology
	}

	// 4. Heuristic check for Binaries (Case-insensitive)
	lowerID := strings.ToLower(assetId)
	if strings.HasSuffix(lowerID, ".exe") || strings.HasSuffix(lowerID, ".dll") || strings.HasSuffix(lowerID, ".so") || strings.Contains(lowerID, "bin/") {
		return graphmodel.NodeTypeBinary
	}

	// 5. Check for Domain structure
	if domainRegex.MatchString(assetId) {
		return graphmodel.NodeTypeDomain
	}

	// 6. Fallback
	return graphmodel.NodeTypeIdentifier
}

// InferAssetType method implementation for the KnowledgeGraph interface.
func (kg *KnowledgeGraph) InferAssetType(assetId string) graphmodel.NodeType {
	return InferAssetType(assetId)
}

// isComparable checks if a value can be used as a map key (required for indexing).
func isComparable(v interface{}) bool {
	if v == nil {
		return true
	}
	t := reflect.TypeOf(v)
	if t == nil {
		return false
	}
	return t.Comparable()
}

// intersectSets returns the intersection of two sets. Optimized to iterate over the smaller set.
func intersectSets(setA, setB Set) Set {
	if setA == nil || setB == nil {
		return make(Set)
	}
	intersection := make(Set)
	if setA.Size() < setB.Size() {
		for item := range setA {
			if setB.Contains(item) {
				intersection.Add(item)
			}
		}
	} else {
		for item := range setB {
			if setA.Contains(item) {
				intersection.Add(item)
			}
		}
	}
	return intersection
}

// idsToNodes converts a Set of IDs back into a slice of Node pointers. (Assumes RLock is held).
func (kg *KnowledgeGraph) idsToNodes(ids Set) []*graphmodel.Node {
	if ids == nil {
		return []*graphmodel.Node{}
	}
	results := make([]*graphmodel.Node, 0, ids.Size())
	for id := range ids {
		if node, exists := kg.nodes[id]; exists {
			// REVISION: Append a clone of the node instead of the direct pointer.
			results = append(results, node.Clone())
		}
	}
	return results
}

