-- migrations/001_initial_schema.sql
-- This migration sets up the initial tables for storing findings and knowledge graph data.

-- Production Ready: Wrap the entire migration in a transaction to ensure atomicity.
BEGIN;

--------------------------------------------------------------------------------
-- Setup, Extensions, and Types
--------------------------------------------------------------------------------

-- Production Ready: Enable pgcrypto for gen_random_uuid().
-- This is the modern standard for UUID generation in Postgres, preferred over uuid-ossp.
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Production Ready: Enable pg_trgm for efficient indexing and searching of TEXT columns (e.g., 'LIKE' queries on target).
CREATE EXTENSION IF NOT EXISTS "pg_trgm";

-- Production Ready: Define an ENUM type for finding severity.
-- This enforces data consistency and improves storage efficiency over VARCHAR.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'finding_severity') THEN
        -- Using standard, lowercase severity levels.
        CREATE TYPE finding_severity AS ENUM ('info', 'low', 'medium', 'high', 'critical');
    END IF;
END
$$;

-- Function to automatically update the 'updated_at' timestamp column.
CREATE OR REPLACE FUNCTION trigger_set_timestamp()
RETURNS TRIGGER AS $$
BEGIN
  -- CURRENT_TIMESTAMP (SQL standard, equivalent to NOW() in Postgres) provides the time at the start of the transaction.
	 NEW.updated_at = CURRENT_TIMESTAMP;
	 RETURN NEW;
END;
$$ LANGUAGE plpgsql;

--------------------------------------------------------------------------------
-- Findings Table
-- Stores all vulnerabilities and informational findings discovered during scans.
--------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS findings (
    -- Using native UUID type and the modern standard UUID generator.
	 	 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	 	 scan_id UUID NOT NULL,
	 	 task_id UUID NOT NULL,

    -- Core Data
    -- Production Ready: Added CHECK constraints to prevent empty strings.
	 	 target TEXT NOT NULL CHECK (target <> ''),
	 	 module VARCHAR(255) NOT NULL CHECK (module <> ''),
	 	 vulnerability VARCHAR(255) NOT NULL CHECK (vulnerability <> ''),

    -- Using the defined ENUM type.
	 	 severity finding_severity NOT NULL,
	 	 description TEXT,
	 	 evidence JSONB, -- Kept nullable, as some findings might lack structured evidence.
	 	 recommendation TEXT,
	 	 cwe VARCHAR(50),

    -- Timestamps
    -- Production Ready: Renamed 'timestamp' to 'observed_at' for clarity (when the scan found it).
	 	 observed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- Production Ready: Added created_at/updated_at for database record tracking.
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Indexing Strategy for Findings

-- Basic lookups
CREATE INDEX IF NOT EXISTS idx_findings_scan_id ON findings (scan_id);
CREATE INDEX IF NOT EXISTS idx_findings_task_id ON findings (task_id);
CREATE INDEX IF NOT EXISTS idx_findings_severity ON findings (severity);
CREATE INDEX IF NOT EXISTS idx_findings_vulnerability ON findings (vulnerability);
CREATE INDEX IF NOT EXISTS idx_findings_module ON findings (module);
CREATE INDEX IF NOT EXISTS idx_findings_observed_at ON findings (observed_at DESC);

-- Production Ready: Composite index for optimizing dashboard queries (e.g., counts by severity for a scan).
CREATE INDEX IF NOT EXISTS idx_findings_scan_severity ON findings (scan_id, severity);

-- Production Ready: Trigram index for the 'target' TEXT column.
-- This significantly speeds up substring searches (e.g., WHERE target LIKE '%example.com%').
DROP INDEX IF EXISTS idx_findings_target; -- Drop the potentially existing B-Tree index from the original script.
CREATE INDEX IF NOT EXISTS idx_findings_target_trgm ON findings USING GIN (target gin_trgm_ops);

-- Production Ready: Partial index for CWE, useful if many findings lack a CWE.
CREATE INDEX IF NOT EXISTS idx_findings_cwe ON findings (cwe) WHERE cwe IS NOT NULL;


-- Production Ready: Apply the updated_at trigger to the findings table idempotently.
DO $$
BEGIN
	 	 IF NOT EXISTS (
	 	 	 	 SELECT 1
	 	 	 	 FROM pg_trigger
	 	 	 	 WHERE tgname = 'set_timestamp_findings'
	 	 	 	 AND tgrelid = 'findings'::regclass
	 	 ) THEN
	 	 	 	 CREATE TRIGGER set_timestamp_findings
	 	 	 	 BEFORE UPDATE ON findings
	 	 	 	 FOR EACH ROW
	 	 	 	 EXECUTE PROCEDURE trigger_set_timestamp();
	 	 END IF;
END
$$;


--------------------------------------------------------------------------------
-- Knowledge Graph (KG) Nodes Table
-- Stores entities (vertices) like URLs, domains, technologies, API endpoints.
--------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS kg_nodes (
	 	 -- ID is TEXT as it often represents natural keys (e.g., URLs, FQDNs).
	 	 id TEXT PRIMARY KEY CHECK (id <> ''),
	 	 type VARCHAR(255) NOT NULL CHECK (type <> ''),
	 	 properties JSONB,
    -- Standardizing timestamp defaults and ensuring NOT NULL.
	 	 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	 	 updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Create a GIN index on the properties column for fast JSONB lookups.
CREATE INDEX IF NOT EXISTS idx_kg_nodes_properties ON kg_nodes USING GIN (properties);
CREATE INDEX IF NOT EXISTS idx_kg_nodes_type ON kg_nodes (type);

-- Idempotent trigger creation for kg_nodes updated_at.
DO $$
BEGIN
	 	 IF NOT EXISTS (
	 	 	 	 SELECT 1
	 	 	 	 FROM pg_trigger
	 	 	 	 WHERE tgname = 'set_timestamp_kg_nodes'
	 	 	 	 AND tgrelid = 'kg_nodes'::regclass
	 	 ) THEN
	 	 	 	 CREATE TRIGGER set_timestamp_kg_nodes
	 	 	 	 BEFORE UPDATE ON kg_nodes
	 	 	 	 FOR EACH ROW
	 	 	 	 EXECUTE PROCEDURE trigger_set_timestamp();
	 	 END IF;
END
$$;


--------------------------------------------------------------------------------
-- Knowledge Graph (KG) Edges Table
-- Stores the relationships (connections) between nodes.
--------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS kg_edges (
	 	 source_id TEXT NOT NULL,
	 	 target_id TEXT NOT NULL,
	 	 relationship VARCHAR(255) NOT NULL CHECK (relationship <> ''),
	 	 properties JSONB,
    -- Timestamp for when this relationship was established.
	 	 timestamp TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

	 	 -- Composite primary key ensures uniqueness of a relationship type between two nodes.
	 	 PRIMARY KEY (source_id, target_id, relationship),

	 	 -- Foreign key constraints ensure data integrity.
	 	 -- ON DELETE CASCADE ensures that if a node is deleted, associated edges are also removed.
	 	 CONSTRAINT fk_edge_source
	 	 	 	 FOREIGN KEY(source_id)
	 	 	 	 REFERENCES kg_nodes(id)
	 	 	 	 ON DELETE CASCADE,
	 	 CONSTRAINT fk_edge_target
	 	 	 	 FOREIGN KEY(target_id)
	 	 	 	 REFERENCES kg_nodes(id)
	 	 	 	 ON DELETE CASCADE
);

-- Create indexes on edge columns for traversing the graph.

-- Production Ready: The Primary Key (source_id, target_id, relationship) already provides an index for lookups starting with source_id.
-- We ensure the redundant explicit index is dropped if it existed from the previous version.
DROP INDEX IF EXISTS idx_kg_edges_source_id;

-- Index needed for reverse lookups (inbound edges).
CREATE INDEX IF NOT EXISTS idx_kg_edges_target_id ON kg_edges (target_id);
-- Index on relationship type for efficient querying of specific relationships.
CREATE INDEX IF NOT EXISTS idx_kg_edges_relationship ON kg_edges (relationship);

COMMIT;