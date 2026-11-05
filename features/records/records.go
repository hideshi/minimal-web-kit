package records

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// Feature は同梱機能の業務記録（records）です。定義（features/records/defs）から、種類ごとの表・画面・MCPのツールを組み立てます。
type Feature struct {
	logger    *slog.Logger
	defs      []RecordDef
	retired   []RetiredDef
	stmts     map[string]*kindStatements
	db        *sql.DB
	admin     features.Admin
	directory features.AccountDirectory
	pages     map[string]*template.Template
	now       func() time.Time
}

// NewFeature は、定義の一覧とやめた種類を受け取ります。定義の検査は Init とコマンドで行います。
func NewFeature(logger *slog.Logger, defs []RecordDef, retired []RetiredDef) *Feature {
	return &Feature{logger: logger, defs: defs, retired: retired, now: time.Now}
}

func (f *Feature) Name() string { return "records" }

// Init は定義を検査し、定義のとおりに表を作り・移します。起動を止める理由があればエラーを返します。
func (f *Feature) Init(db *sql.DB) error {
	if err := Migrate(context.Background(), db, f.defs, f.retired, f.logger); err != nil {
		return err
	}
	f.db, f.stmts = db, buildRecordStatements(f.defs, f.retired)
	return nil
}

// RegisterRoutes はCoreの経路に何も登録しません。
func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}

// ReadOnlyCommands は、DBを読むだけの確認のコマンドです。
func (f *Feature) ReadOnlyCommands() []features.ReadOnlyCommand {
	return []features.ReadOnlyCommand{{
		Name:    "records-check",
		Summary: "Show what this executable will do to the records tables on start (reads the database only)",
		Run: func(ctx context.Context, dbPath string, args []string, out io.Writer) error {
			if len(args) != 0 {
				return errors.New("usage: records-check")
			}
			return runRecordsCheck(ctx, dbPath, f.defs, f.retired, out)
		},
	}}
}

// Commands は、やめた種類の表を消すコマンドです。
func (f *Feature) Commands() []features.Command {
	return []features.Command{{
		Name:    "records-drop",
		Summary: "Drop the table of a retired records kind (takes a copy first)",
		Run: func(ctx context.Context, db *sql.DB, args []string, in io.Reader, out io.Writer) error {
			if len(args) != 1 {
				return errors.New("usage: records-drop <kind>")
			}
			return dropRecordsTable(ctx, db, args[0], f.defs, f.retired, out)
		},
	}}
}

// dropRecordsTable は、やめた種類の表を消します。複製を作ってから、表を消し、種類の記録を dropped にします（1つのトランザクション）。
// 表が無ければ何もしません。対象は RetiredDefs にある種類だけです。
func dropRecordsTable(ctx context.Context, db *sql.DB, name string, defs []RecordDef, retired []RetiredDef, out io.Writer) error {
	if err := validateRecordDefs(defs, retired); err != nil {
		return err
	}
	isRetired := false
	for _, r := range retired {
		if r.Name == name {
			isRetired = true
		}
	}
	if !isRetired {
		return fmt.Errorf("%q is not in RetiredDefs; only retired kinds can be dropped", name)
	}
	if _, err := db.ExecContext(ctx, migrationSQL); err != nil {
		return err
	}
	st, err := readRecordsState(ctx, db)
	if err != nil {
		return err
	}
	t := tableName(name)
	if !st.tables[t] {
		if st.states[t] == "active" {
			return fmt.Errorf("%s: records_tables says active but the table is missing; restore the database from a backup", t)
		}
		fmt.Fprintf(out, "%s does not exist; nothing to do.\n", t)
		return nil
	}
	path, err := store.BackupBeforeMigration(ctx, db, "records-drop")
	if err != nil {
		return fmt.Errorf("backup before drop: %w", err)
	}
	drop := buildRecordStatements(defs, retired)[name].drop
	err = store.Atomic(ctx, db, func(c *sql.Conn) error {
		if _, err := execStatement(ctx, c, drop); err != nil {
			return err
		}
		_, err := c.ExecContext(ctx, "INSERT INTO records_tables (table_name, state) VALUES (?, 'dropped') ON CONFLICT (table_name) DO UPDATE SET state = 'dropped'", t)
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Dropped %s. Copy taken before the drop: %s\n", t, filepath.Base(path))
	fmt.Fprintf(out, "Set Dropped: true for %q in RetiredDefs and deploy. To undo, restore the database from that copy.\n", name)
	return nil
}
