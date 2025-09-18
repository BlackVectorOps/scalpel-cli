package store

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"go.uber.org/zap"
)

// -- Test Cases --

func TestNewStore(t *testing.T) {
	t.Run("should return error if ping fails", func(t *testing.T) {
		mockPool, err := pgxmock.NewPool(pgxmock.MonitorPingsOption(true))
		require.NoError(t, err)
		defer mockPool.Close()

		pingErr := errors.New("database unavailable")
		mockPool.ExpectPing().WillReturnError(pingErr)

		_, err = New(context.Background(), mockPool, zap.NewNop())
		require.Error(t, err)
		assert.ErrorIs(t, err, pingErr, "Error from ping should be propagated")
		assert.NoError(t, mockPool.ExpectationsWereMet())
	})
}

func TestPersistData(t *testing.T) {
	ctx := context.Background()

	t.Run("should persist a full envelope successfully", func(t *testing.T) {
		mockPool, err := pgxmock.NewPool(pgxmock.MonitorPingsOption(true))
		require.NoError(t, err)
		defer mockPool.Close()

		mockPool.ExpectPing().WillReturnError(nil)
		store, err := New(context.Background(), mockPool, zap.NewNop())
		require.NoError(t, err)

		scanID := uuid.NewString()
		finding := schemas.Finding{ID: "finding-1", Vulnerability: schemas.Vulnerability{Name: "XSS"}, Evidence: "{}"}
		node := schemas.NodeInput{ID: "node-1", Type: schemas.NodeURL}
		edge := schemas.EdgeInput{ID: "edge-1", From: "node-1", To: "node-2", Type: "LINKS_TO"}

		envelope := &schemas.ResultEnvelope{
			ScanID:   scanID,
			Findings: []schemas.Finding{finding},
			KGUpdates: &schemas.KnowledgeGraphUpdate{
				NodesToAdd: []schemas.NodeInput{node},
				EdgesToAdd: []schemas.EdgeInput{edge},
			},
		}

		mockPool.ExpectBegin()

		// -- findings (Uses CopyFrom) --
		findingColumns := []string{"id", "scan_id", "task_id", "target", "module", "vulnerability", "severity", "description", "evidence", "recommendation", "cwe", "observed_at"}
		mockPool.ExpectCopyFrom(pgx.Identifier{"findings"}, findingColumns).WillReturnResult(1)

		// -- nodes (Uses Exec loop) --
		// SIMPLIFIED: We now just expect a simple Exec call.
		sqlNodes := `
		INSERT INTO kg_nodes (id, type, properties, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			type = EXCLUDED.type,
			properties = kg_nodes.properties || EXCLUDED.properties,
			updated_at = EXCLUDED.updated_at;
	`
		mockPool.ExpectExec(regexp.QuoteMeta(sqlNodes)).
			WithArgs(node.ID, string(node.Type), json.RawMessage("{}"), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))

		// -- edges (Uses Exec loop) --
		sqlEdges := `
		INSERT INTO kg_edges (source_id, target_id, relationship, properties, "timestamp")
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (source_id, target_id, relationship) DO UPDATE SET
			properties = kg_edges.properties || EXCLUDED.properties;
	`
		mockPool.ExpectExec(regexp.QuoteMeta(sqlEdges)).
			WithArgs(edge.From, edge.To, string(edge.Type), json.RawMessage("{}"), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))

		mockPool.ExpectCommit()

		if err := store.PersistData(ctx, envelope); err != nil {
			t.Fatalf("PersistData failed: %v", err)
		}
		assert.NoError(t, mockPool.ExpectationsWereMet())
	})

	t.Run("should handle transaction begin failure", func(t *testing.T) {
		mockPool, err := pgxmock.NewPool(pgxmock.MonitorPingsOption(true))
		require.NoError(t, err)
		defer mockPool.Close()

		mockPool.ExpectPing().WillReturnError(nil)
		store, err := New(context.Background(), mockPool, zap.NewNop())
		require.NoError(t, err)

		beginErr := errors.New("cannot begin tx")
		mockPool.ExpectBegin().WillReturnError(beginErr)

		err = store.PersistData(ctx, &schemas.ResultEnvelope{})
		require.Error(t, err)
		assert.ErrorIs(t, err, beginErr)
		assert.NoError(t, mockPool.ExpectationsWereMet())
	})

	t.Run("should rollback if persisting findings fails", func(t *testing.T) {
		mockPool, err := pgxmock.NewPool(pgxmock.MonitorPingsOption(true))
		require.NoError(t, err)
		defer mockPool.Close()

		mockPool.ExpectPing().WillReturnError(nil)
		store, err := New(context.Background(), mockPool, zap.NewNop())
		require.NoError(t, err)

		copyErr := errors.New("copy from failed")
		envelope := &schemas.ResultEnvelope{Findings: []schemas.Finding{{ID: "f-1", Vulnerability: schemas.Vulnerability{Name: "Test"}, Evidence: "{}"}}}

		mockPool.ExpectBegin()
		findingColumns := []string{"id", "scan_id", "task_id", "target", "module", "vulnerability", "severity", "description", "evidence", "recommendation", "cwe", "observed_at"}
		mockPool.ExpectCopyFrom(pgx.Identifier{"findings"}, findingColumns).
			WillReturnError(copyErr)
		mockPool.ExpectRollback()

		err = store.PersistData(ctx, envelope)
		require.Error(t, err)
		assert.ErrorIs(t, err, copyErr)
		assert.NoError(t, mockPool.ExpectationsWereMet())
	})
}

func TestGetFindingsByScanID(t *testing.T) {
	ctx := context.Background()

	t.Run("should retrieve findings successfully", func(t *testing.T) {
		mockPool, err := pgxmock.NewPool(pgxmock.MonitorPingsOption(true))
		require.NoError(t, err)
		defer mockPool.Close()

		mockPool.ExpectPing().WillReturnError(nil)
		store, err := New(context.Background(), mockPool, zap.NewNop())
		require.NoError(t, err)

		sqlGetFindings := `
		SELECT id, task_id, observed_at, target, module, vulnerability, severity, description, evidence, recommendation, cwe
		FROM findings
		WHERE scan_id = $1
		ORDER BY observed_at ASC;
		`
		scanID := uuid.NewString()
		now := time.Now()
		// Provide the evidence as json.RawMessage (a raw string literal is easiest)
		evidenceJSON := json.RawMessage(`{"detail": "some evidence"}`)

		columns := []string{"id", "task_id", "observed_at", "target", "module", "vulnerability", "severity", "description", "evidence", "recommendation", "cwe"}
		rows := pgxmock.NewRows(columns).
			AddRow("finding-123", "task-abc", now, "https://example.com", "SQLAnalyzer", "SQLi", "High", "desc", evidenceJSON, "reco", []string{"CWE-89"})

		// Use a flexible regex for the query
		sqlRegex := regexp.QuoteMeta(sqlGetFindings)
		sqlRegex = regexp.MustCompile(`\s+`).ReplaceAllString(sqlRegex, `\s+`)
		mockPool.ExpectQuery(sqlRegex).
			WithArgs(scanID).
			WillReturnRows(rows)

		findings, err := store.GetFindingsByScanID(ctx, scanID)
		require.NoError(t, err)
		require.Len(t, findings, 1)

		assert.Equal(t, "finding-123", findings[0].ID)
		assert.Equal(t, "SQLi", findings[0].Vulnerability.Name)
		assert.Equal(t, json.RawMessage(`{"detail": "some evidence"}`), findings[0].Evidence)
		assert.NoError(t, mockPool.ExpectationsWereMet())
	})
}