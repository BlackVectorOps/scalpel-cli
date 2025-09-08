package schemas

import "time"

// -- Core Graph Models --
// These types represent the fully-formed entities as they exist in the graph database.

// Node represents a fundamental entity in the knowledge graph.
type Node struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Label      string                 `json:"label"`
	Status     string                 `json:"status"`
	CreatedAt  time.Time              `json:"created_at"`
	LastSeen   time.Time              `json:"last_seen"`
	Properties map[string]interface{} `json:"properties"`
}

// Edge represents a relationship between two Nodes.
type Edge struct {
	ID         string                 `json:"id"`
	From       string                 `json:"from"`
	To         string                 `json:"to"`
	Type       string                 `json:"type"`
	Label      string                 `json:"label"`
	CreatedAt  time.Time              `json:"created_at"`
	LastSeen   time.Time              `json:"last_seen"`
	Properties map[string]interface{} `json:"properties"`
}

// -- Graph Input/Update Models --
// These types are used specifically as inputs for creating or updating graph elements.

// NodeInput is used for creating or updating a node for the Knowledge Graph.
type NodeInput struct {
	ID         string
	Type       NodeType
	Properties Properties
}

// EdgeInput is used for creating or updating an edge for the Knowledge Graph.
type EdgeInput struct {
	SourceID     string
	TargetID     string
	Relationship RelationshipType
	Properties   Properties
}

// NodeType defines the categories of entities in the graph.
type NodeType string

// RelationshipType defines the nature of the connection between nodes.
type RelationshipType string

// Properties is a generic map for storing attributes.
type Properties map[string]interface{}

// -- Graph Query/Export Models --
// These types are used to fulfill the KnowledgeGraph interface contracts.

// Query is a placeholder for graph query structures.
type Query struct{}

// NeighborsResult is a placeholder for the results of a neighbor query.
type NeighborsResult struct{}

// GraphExport is a placeholder for a full graph export.
type GraphExport struct{}
