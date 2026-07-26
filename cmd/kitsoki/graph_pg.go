package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"kitsoki/internal/graph"
	"kitsoki/internal/graph/pgcatalog"
)

// graph_pg.go — the YAML<->Postgres catalog bridge commands:
//
//	kitsoki graph import --catalog <path.yaml> --catalog-id <id> [--force]
//	kitsoki graph export --catalog-id <id> [--out <path.yaml>]
//
// Both resolve the shared db backend exactly like every other satellite store
// (openGraphCatalogDB in db_backend.go): sqlite — the default — is a clear
// error, because graph catalogs have no SQLite backend and the YAML file
// catalog remains the default surface. The commands are thin cobra wrappers;
// the logic lives in graphPGImport / graphPGExportYAML, which take a *sql.DB
// so tests drive them against pgtest databases directly.

func graphImportCmd() *cobra.Command {
	var catalogPath string
	var catalogID string
	var force bool
	cmd := &cobra.Command{
		Use:   "import --catalog <path.yaml> --catalog-id <id>",
		Short: "Import a YAML catalog into the Postgres graph catalog store",
		Long: `Loads the YAML catalog at --catalog (single file or bundle dir, exactly
what "graph lint" loads) and persists it under --catalog-id in the configured
Postgres backend (--db-backend postgres|embedded-postgres) at revision 1.

Re-import is idempotent while the stored catalog is still at revision 1 (the
initial import, never committed to): the rows are simply replaced. Once the
stored catalog has commits beyond the import (revision > 1), re-importing
would clobber them, so it is refused unless --force is passed. The append-only
graph audit history survives either way.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Backend first: a sqlite default must fail fast and clearly,
			// before any catalog file is even read.
			s, err := openGraphCatalogDB()
			if err != nil {
				return fmt.Errorf("graph import: %w", err)
			}
			defer func() { _ = s.Close() }()

			res, err := graphPGImport(cmd.Context(), s.DB(), catalogPath, catalogID, force)
			if err != nil {
				return fmt.Errorf("graph import: %w", err)
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			out := cmd.OutOrStdout()
			if res.Replaced {
				fmt.Fprintf(out, "graph import: replaced pg catalog %q (was rev %d) with %s: %d node(s), %d type(s) at rev 1\n",
					catalogID, res.PrevRev, catalogPath, res.Nodes, res.Types)
				return nil
			}
			fmt.Fprintf(out, "graph import: imported %s as pg catalog %q: %d node(s), %d type(s) at rev 1\n",
				catalogPath, catalogID, res.Nodes, res.Types)
			return nil
		},
	}
	cmd.Flags().StringVar(&catalogPath, "catalog", "", "YAML catalog to import (single file or bundle dir)")
	cmd.Flags().StringVar(&catalogID, "catalog-id", "", "catalog id to store it under in Postgres")
	cmd.Flags().BoolVar(&force, "force", false, "replace a stored catalog even when it has commits beyond the initial import (rev > 1)")
	_ = cmd.MarkFlagRequired("catalog")
	_ = cmd.MarkFlagRequired("catalog-id")
	return cmd
}

func graphExportCmd() *cobra.Command {
	var catalogID string
	var outPath string
	cmd := &cobra.Command{
		Use:   "export --catalog-id <id> [--out <path.yaml>]",
		Short: "Export a Postgres graph catalog as a deterministic single-file YAML catalog",
		Long: `Loads --catalog-id from the configured Postgres backend and writes it as a
single-file YAML catalog (the same shape LoadCatalog reads): stable key order,
nodes in sorted-id order, 2-space indent. The output is deterministic — the
same stored catalog always exports byte-identically — so it is suitable for
review, backup, and re-import via "graph import".

Writes to stdout unless --out is given.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openGraphCatalogDB()
			if err != nil {
				return fmt.Errorf("graph export: %w", err)
			}
			defer func() { _ = s.Close() }()

			raw, err := graphPGExportYAML(cmd.Context(), s.DB(), catalogID)
			if err != nil {
				return fmt.Errorf("graph export: %w", err)
			}
			if outPath == "" {
				_, err := cmd.OutOrStdout().Write(raw)
				return err
			}
			if err := os.WriteFile(outPath, raw, 0o644); err != nil {
				return fmt.Errorf("graph export: write %s: %w", outPath, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "graph export: wrote pg catalog %q to %s\n", catalogID, outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&catalogID, "catalog-id", "", "catalog id to export from Postgres")
	cmd.Flags().StringVar(&outPath, "out", "", "output file (default: stdout)")
	_ = cmd.MarkFlagRequired("catalog-id")
	return cmd
}

// graphImportResult reports what graphPGImport did.
type graphImportResult struct {
	Nodes, Types int
	Warnings     []string
	// Replaced is true when an existing stored catalog was replaced (an
	// idempotent rev-1 re-import, or --force); PrevRev is its revision.
	Replaced bool
	PrevRev  int64
}

// graphPGImport loads the YAML catalog at catalogPath and persists it under
// catalogID at revision 1. An existing stored catalog still at revision 1 is
// replaced (idempotent re-import); one with commits beyond the import
// (rev > 1) is refused unless force.
func graphPGImport(ctx context.Context, db *sql.DB, catalogPath, catalogID string, force bool) (graphImportResult, error) {
	var res graphImportResult

	cat, err := graph.LoadCatalog(catalogPath)
	if err != nil {
		return res, err
	}
	res.Nodes = len(cat.Nodes)
	res.Types = len(cat.Registry.All())
	res.Warnings = cat.Warnings

	// Open runs the idempotent schema migration and validates the arguments,
	// so the rev probe below never races an absent schema.
	if _, err := pgcatalog.Open(db, catalogID); err != nil {
		return res, err
	}

	var rev int64
	err = db.QueryRowContext(ctx,
		`SELECT rev FROM graph.graph_catalogs WHERE catalog_id = $1`, catalogID).Scan(&rev)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Fresh import.
	case err != nil:
		return res, fmt.Errorf("probe pg catalog %q: %w", catalogID, err)
	default:
		if rev > 1 && !force {
			return res, fmt.Errorf("pg catalog %q is at rev %d — it has commits beyond the initial import; refusing to clobber them (pass --force to replace)", catalogID, rev)
		}
		// Idempotent rev-1 re-import, or --force: drop the stored rows
		// (graph_types/graph_nodes/graph_edges cascade off the catalog row;
		// the append-only graph_audit history is deliberately kept).
		if _, err := db.ExecContext(ctx,
			`DELETE FROM graph.graph_catalogs WHERE catalog_id = $1`, catalogID); err != nil {
			return res, fmt.Errorf("replace pg catalog %q: %w", catalogID, err)
		}
		res.Replaced = true
		res.PrevRev = rev
	}

	if err := pgcatalog.ImportCatalog(ctx, db, cat, catalogID); err != nil {
		return res, err
	}
	return res, nil
}

// graphPGExportYAML loads catalogID from Postgres and renders it as a
// deterministic single-file YAML catalog.
func graphPGExportYAML(ctx context.Context, db *sql.DB, catalogID string) ([]byte, error) {
	st, err := pgcatalog.Open(db, catalogID)
	if err != nil {
		return nil, err
	}
	cat, _, err := st.Load(ctx)
	if err != nil {
		return nil, err
	}
	return renderCatalogExportYAML(cat)
}

// --- deterministic single-file rendering ---------------------------------
//
// The export mirrors the loader's own file shapes (fileSingleCatalog /
// fileTypeDef / fileNode in internal/graph/loader.go): the catalog's wire
// mappings (Catalog.ExportData) are reshaped through these mirror structs and
// yaml.v3-encoded, so key order is the struct declaration order — stable —
// and every map the encoder meets (edges, inline type-specific fields,
// nested field values) is emitted with yaml.v3's sorted keys. Node order is
// SortedNodeIDs (ExportData's contract); indent is 2 spaces, the same
// convention marshalYAMLNode (internal/graph/apply.go) enforces for
// hand-authored catalogs. The result is byte-deterministic for a given
// stored catalog, and loads back through graph.LoadCatalog unchanged.

type exportEdgeField struct {
	ID          string `yaml:"id"`
	TargetType  string `yaml:"target_type,omitempty"`
	Cardinality string `yaml:"cardinality"`
	Storage     string `yaml:"storage,omitempty"`
	Acyclic     bool   `yaml:"acyclic,omitempty"`
	Renders     bool   `yaml:"renders,omitempty"`
	NestsUnder  bool   `yaml:"nests_under,omitempty"`
}

type exportMaterializeParam struct {
	ID          string   `yaml:"id"`
	Type        string   `yaml:"type,omitempty"`
	Default     any      `yaml:"default"` // no omitempty: an authored `default: false` must survive
	Values      []string `yaml:"values,omitempty"`
	Required    bool     `yaml:"required,omitempty"`
	SourceField string   `yaml:"source_field,omitempty"`
	SourceEdge  string   `yaml:"source_edge,omitempty"`
}

type exportMaterializeCheck struct {
	ID           string         `yaml:"id"`
	Script       string         `yaml:"script,omitempty"`
	ScriptField  string         `yaml:"script_field,omitempty"`
	Inputs       map[string]any `yaml:"inputs,omitempty"`
	InputsField  string         `yaml:"inputs_field,omitempty"`
	Capabilities map[string]any `yaml:"capabilities,omitempty"`
}

type exportMaterializeDecl struct {
	Story                string                   `yaml:"story,omitempty"`
	ContextEdges         []string                 `yaml:"context_edges,omitempty"`
	IncomingContextEdges []string                 `yaml:"incoming_context_edges,omitempty"`
	Params               []exportMaterializeParam `yaml:"params,omitempty"`
	Gates                []string                 `yaml:"gates,omitempty"`
	Checks               []exportMaterializeCheck `yaml:"checks,omitempty"`
}

type exportArtifactDecl struct {
	Schema       string `yaml:"schema,omitempty"`
	Format       string `yaml:"format,omitempty"`
	Presentation string `yaml:"presentation,omitempty"`
}

type exportTypeDef struct {
	ID             string                 `yaml:"id"`
	Schema         string                 `yaml:"schema,omitempty"`
	Extends        string                 `yaml:"extends,omitempty"`
	DerivesFrom    *string                `yaml:"derives_from,omitempty"`
	Summary        string                 `yaml:"summary,omitempty"`
	RequiredFields []string               `yaml:"required_fields,omitempty"`
	EdgeFields     []exportEdgeField      `yaml:"edge_fields,omitempty"`
	Artifact       *exportArtifactDecl    `yaml:"artifact,omitempty"`
	Materialize    *exportMaterializeDecl `yaml:"materialize,omitempty"`
}

type exportNode struct {
	Schema     string              `yaml:"schema"`
	ID         string              `yaml:"id"`
	Title      string              `yaml:"title"`
	Status     string              `yaml:"status"`
	Visibility string              `yaml:"visibility"`
	Sources    []string            `yaml:"sources,omitempty"`
	Edges      map[string][]string `yaml:"edges,omitempty"`
	Extra      map[string]any      `yaml:",inline"`
}

type exportSingleCatalog struct {
	Schema          string                 `yaml:"schema"`
	TypeRegistry    []exportTypeDef        `yaml:"type_registry"`
	LintHints       *graph.LintHints       `yaml:"lint_hints,omitempty"`
	WritePolicy     *graph.WritePolicy     `yaml:"write_policy,omitempty"`
	FeedbackRouting *graph.FeedbackRouting `yaml:"feedback_routing,omitempty"`
	Nodes           []exportNode           `yaml:"nodes"`
}

// renderCatalogExportYAML serializes cat into the deterministic single-file
// shape described above.
func renderCatalogExportYAML(cat *graph.Catalog) ([]byte, error) {
	data, err := cat.ExportData()
	if err != nil {
		return nil, err
	}
	doc := exportSingleCatalog{
		Schema:          string(data.Schema),
		LintHints:       data.LintHints,
		WritePolicy:     data.WritePolicy,
		FeedbackRouting: data.FeedbackRouting,
	}
	for _, tw := range data.Types {
		var td exportTypeDef
		if err := reshapeExportYAML(tw, &td); err != nil {
			return nil, fmt.Errorf("type entry: %w", err)
		}
		doc.TypeRegistry = append(doc.TypeRegistry, td)
	}
	for _, nw := range data.Nodes {
		var n exportNode
		if err := reshapeExportYAML(nw, &n); err != nil {
			return nil, fmt.Errorf("node entry: %w", err)
		}
		doc.Nodes = append(doc.Nodes, n)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// reshapeExportYAML converts a wire mapping into one of the export mirror
// structs via a YAML round trip — the same decode path the on-disk files
// take, so the export can never interpret wire data differently from the
// loader (the counterpart of internal/graph's reshapeWire).
func reshapeExportYAML(src, dst any) error {
	raw, err := yaml.Marshal(src)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(raw, dst)
}
