// internal/knowledgegraph/postgres_kg.go

package knowledgegraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

// DBPool defines an interface that abstracts the necessary pgxpool.Pool methods.
// This is a slick move because it allows us to easily mock the database pool during
// testing, isolating our knowledge graph logic from the actual database.
type DBPool interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Close()
}

// Just to be sure, we'll make the compiler check that *pgxpool.Pool actually
// satisfies our DBPool interface. If it doesn't, we'll know at compile time.
var _ DBPool = (*pgxpool.Pool)(nil)

// PostgresKG provides a robust, persistent implementation of the schemas.KnowledgeGraphClient
// interface using a PostgreSQL backend. This implementation leverages pgx for performance.
type PostgresKG struct {
	// We're using our DBPool interface here instead of a concrete *pgxpool.Pool.
	// This makes our struct more flexible and much easier to test.
	pool DBPool
}

// NewPostgresKG initializes a new connection wrapper for the PostgreSQL database.
// It takes our DBPool interface, allowing it to work with a real pgxpool or a mock.
func NewPostgresKG(pool DBPool) *PostgresKG {
	return &PostgresKG{pool: pool}
}

// AddNode inserts a new node or updates an existing one in the database.
// It uses an ON CONFLICT clause to handle nodes that already exist, which is a
// clean way to perform an "upsert" operation.
func (p *PostgresKG) AddNode(ctx context.Context, node schemas.Node) error {
	props, err := json.Marshal(node.Properties)
	if err != nil {
		return fmt.Errorf("failed to marshal node properties: %w", err)
	}

	// Switched from db.ExecContext to pool.Exec. pgx methods are context aware
	// by default, so we pass the context as the first argument.
	_, err = p.pool.Exec(ctx, `
		INSERT INTO nodes (id, type, label, status, properties, created_at, last_seen)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET
			type = EXCLUDED.type,
			label = EXCLUDED.label,
			status = EXCLUDED.status,
			properties = EXCLUDED.properties,
			last_seen = EXCLUDED.last_seen;
	`, node.ID, node.Type, node.Label, node.Status, props, node.CreatedAt, time.Now())

	return err
}

// AddEdge inserts a new edge or updates an existing one, linking two nodes.
func (p *PostgresKG) AddEdge(ctx context.Context, edge schemas.Edge) error {
	props, err := json.Marshal(edge.Properties)
	if err != nil {
		return fmt.Errorf("failed to marshal edge properties: %w", err)
	}

	_, err = p.pool.Exec(ctx, `
		INSERT INTO edges (id, from_node, to_node, type, label, properties, created_at, last_seen)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO UPDATE SET
			from_node = EXCLUDED.from_node,
			to_node = EXCLUDED.to_node,
			type = EXCLUDED.type,
			label = EXCLUDED.label,
			properties = EXCLUDED.properties,
			last_seen = EXCLUDED.last_seen;
	`, edge.ID, edge.From, edge.To, edge.Type, edge.Label, props, edge.CreatedAt, time.Now())

	return err
}

// GetNode fetches a single node from the database using its unique ID.
func (p *PostgresKG) GetNode(ctx context.Context, id string) (schemas.Node, error) {
	var node schemas.Node
	var props []byte

	// Replaced db.QueryRowContext with pool.QueryRow.
	err := p.pool.QueryRow(ctx, `
		SELECT id, type, label, status, properties, created_at, last_seen
		FROM nodes WHERE id = $1;
	`, id).Scan(&node.ID, &node.Type, &node.Label, &node.Status, &props, &node.CreatedAt, &node.LastSeen)

	if err != nil {
		// Important: we now check for pgx.ErrNoRows instead of the standard
		// library's sql.ErrNoRows. Using errors.Is is the modern, safe way to check.
		if errors.Is(err, pgx.ErrNoRows) {
			return schemas.Node{}, fmt.Errorf("node with id '%s' not found", id)
		}
		return schemas.Node{}, err
	}

	if err = json.Unmarshal(props, &node.Properties); err != nil {
		return schemas.Node{}, fmt.Errorf("failed to unmarshal node properties: %w", err)
	}

	return node, nil
}

// GetNeighbors retrieves all nodes that are directly connected from the given node.
// This is essential for traversing the graph and understanding relationships.
func (p *PostgresKG) GetNeighbors(ctx context.Context, nodeID string) ([]schemas.Node, error) {
	// Replaced db.QueryContext with pool.Query. The row handling logic
	// (defer rows.Close(), rows.Next(), rows.Scan(), rows.Err()) is nicely
	// consistent between database/sql and pgx.
	rows, err := p.pool.Query(ctx, `
		SELECT n.id, n.type, n.label, n.status, n.properties, n.created_at, n.last_seen
		FROM nodes n
		JOIN edges e ON n.id = e.to_node
		WHERE e.from_node = $1;
	`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var neighbors []schemas.Node
	for rows.Next() {
		var node schemas.Node
		var props []byte
		if err := rows.Scan(&node.ID, &node.Type, &node.Label, &node.Status, &props, &node.CreatedAt, &node.LastSeen); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(props, &node.Properties); err != nil {
			return nil, fmt.Errorf("failed to unmarshal neighbor node properties: %w", err)
		}
		neighbors = append(neighbors, node)
	}

	// Always good practice to check for any errors that occurred during iteration.
	return neighbors, rows.Err()
}

// GetEdges finds all outgoing edges originating from a specific node.
func (p *PostgresKG) GetEdges(ctx context.Context, nodeID string) ([]schemas.Edge, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, from_node, to_node, type, label, properties, created_at, last_seen
		FROM edges WHERE from_node = $1;
	`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []schemas.Edge
	for rows.Next() {
		var edge schemas.Edge
		var props []byte
		if err := rows.Scan(&edge.ID, &edge.From, &edge.To, &edge.Type, &edge.Label, &props, &edge.CreatedAt, &edge.LastSeen); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(props, &edge.Properties); err != nil {
			return nil, fmt.Errorf("failed to unmarshal edge properties: %w", err)
		}
		edges = append(edges, edge)
	}

	return edges, rows.Err()
}