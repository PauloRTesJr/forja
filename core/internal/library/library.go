// Package library is the core's durable state: jobs, their stages, the stage cache index,
// and generated assets. SQLite in WAL mode, pure Go (no cgo).
package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Status string

const (
	Queued  Status = "queued"
	Running Status = "running"
	// Waiting jobs need the user's answer (does this image hold several views?). They hold no
	// slot while they wait, and keep waiting across restarts.
	Waiting   Status = "waiting"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
)

func (s Status) Done() bool { return s == Succeeded || s == Failed || s == Cancelled }

type Output struct {
	Hash    string `json:"hash"`
	Kind    string `json:"kind"`
	Preview bool   `json:"preview,omitempty"`
	// Note is what the stage said about the output for the person, such as a stand-in made when
	// it could not do what it was asked. The stage cache keeps it with the output.
	Note string `json:"note,omitempty"`
}

type Stage struct {
	ID       string   `json:"id"`
	Use      string   `json:"use"`
	Model    string   `json:"model,omitempty"`
	Status   Status   `json:"status"`
	Key      string   `json:"key"`
	Cached   bool     `json:"cached"`
	Provider string   `json:"provider,omitempty"`
	Outputs  []Output `json:"outputs"`
	Seconds  float64  `json:"seconds"`
	PeakVRAM int64    `json:"peak_vram_bytes,omitempty"`
	Step     int      `json:"step"`
	Total    int      `json:"total"`
	Error    string   `json:"error,omitempty"`
	// Note is what the provider last said about the running stage, such as its place in a
	// hosted model's queue.
	Note string `json:"note,omitempty"`
	// Fallbacks are the providers the stage left after failing there, oldest first.
	Fallbacks []Fallback `json:"fallbacks,omitempty"`
	// ProviderLabel names Provider for people, as it was called when the job ran.
	ProviderLabel string `json:"provider_label,omitempty"`
}

// OutputNotes are what the stage said about its outputs, such as a stand-in it made.
func (s *Stage) OutputNotes() []string {
	var notes []string
	for _, o := range s.Outputs {
		if o.Note != "" {
			notes = append(notes, o.Note)
		}
	}
	return notes
}

// Fallback is a stage moving to another provider after a failure there.
type Fallback struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Reason    string `json:"reason"`
	FromLabel string `json:"from_label,omitempty"`
	ToLabel   string `json:"to_label,omitempty"`
}

type Job struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Preset string `json:"preset"`
	Input  string `json:"input"`
	// Views are the job's images of its subject by view ("left", "back", "right"). A "front"
	// stands in for Input, as when the views were cut out of a sheet.
	Views       map[string]string `json:"views,omitempty"`
	Sheet       *Sheet            `json:"sheet,omitempty"`
	Seed        int               `json:"seed"`
	Project     string            `json:"project,omitempty"`
	Priority    string            `json:"priority"`
	Overrides   map[string]any    `json:"overrides,omitempty"`
	Status      Status            `json:"status"`
	Error       string            `json:"error,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	FinishedAt  *time.Time        `json:"finished_at,omitempty"`
	AssetID     string            `json:"asset_id,omitempty"`
	Stages      []Stage           `json:"stages"`
	EstimateSec float64           `json:"estimate_seconds,omitempty"`
}

// Front is the image the job's front-view stages read.
func (j *Job) Front() string {
	if f := j.Views["front"]; f != "" {
		return f
	}
	return j.Input
}

// SideViews lists the job's views other than the front, in no particular order.
func (j *Job) SideViews() []string {
	var out []string
	for v := range j.Views {
		if v != "front" {
			out = append(out, v)
		}
	}
	return out
}

// What a job does when its image holds several views of one subject.
const (
	SheetAsk   = "ask"   // wait for the user to confirm or relabel the views
	SheetSplit = "split" // use the proposed views
	SheetKeep  = "keep"  // use the image whole; what was found is only recorded
)

// Sheet is what the views.detect stage found in a job's image (docs/decisions.md, 17).
type Sheet struct {
	Mode    string `json:"mode"`
	Checked bool   `json:"checked"`
	Found   bool   `json:"found"`
	// Cutout is the background-removal output the boxes are in; Size is its size in pixels.
	Cutout string      `json:"cutout,omitempty"`
	Size   [2]int      `json:"size"`
	Parts  []SheetPart `json:"parts,omitempty"`
	// Answer is SheetSplit or SheetKeep once decided; Chosen are the views a split cut out.
	Answer string      `json:"answer,omitempty"`
	Chosen []SheetPart `json:"chosen,omitempty"`
}

// SheetPart is one region of a sheet: Kind is "figure" or "detail" (a close-up cut by its
// frame), View the view it shows, or empty to leave it out.
type SheetPart struct {
	Box  [4]int `json:"box"`
	Kind string `json:"kind,omitempty"`
	View string `json:"view"`
}

type Asset struct {
	ID         string         `json:"id"`
	JobID      string         `json:"job_id"`
	Name       string         `json:"name"`
	Preset     string         `json:"preset"`
	GLB        string         `json:"glb"`
	Input      string         `json:"input"`
	Thumbnail  string         `json:"thumbnail,omitempty"`
	Stats      map[string]any `json:"stats,omitempty"`
	Project    string         `json:"project,omitempty"`
	ExportPath string         `json:"export_path,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

type DB struct {
	sql *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL,
	finished_at INTEGER,
	status TEXT NOT NULL,
	name TEXT NOT NULL,
	preset TEXT NOT NULL,
	input TEXT NOT NULL,
	project TEXT NOT NULL DEFAULT '',
	priority TEXT NOT NULL DEFAULT 'batch',
	overrides TEXT NOT NULL DEFAULT '{}',
	stages TEXT NOT NULL DEFAULT '[]',
	asset_id TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	views TEXT NOT NULL DEFAULT '{}',
	sheet TEXT NOT NULL DEFAULT '',
	seed INTEGER NOT NULL DEFAULT 42
);
CREATE INDEX IF NOT EXISTS jobs_created ON jobs(created_at DESC);
CREATE TABLE IF NOT EXISTS stage_cache (
	key TEXT PRIMARY KEY,
	outputs TEXT NOT NULL,
	seconds REAL NOT NULL,
	created_at INTEGER NOT NULL,
	hits INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS stage_timings (
	use TEXT NOT NULL,
	model TEXT NOT NULL,
	provider TEXT NOT NULL,
	seconds REAL NOT NULL,
	at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS stage_timings_lookup ON stage_timings(use, model, provider, at DESC);
CREATE TABLE IF NOT EXISTS assets (
	id TEXT PRIMARY KEY,
	job_id TEXT NOT NULL,
	name TEXT NOT NULL,
	preset TEXT NOT NULL,
	glb TEXT NOT NULL,
	input TEXT NOT NULL,
	thumbnail TEXT NOT NULL DEFAULT '',
	stats TEXT NOT NULL DEFAULT '{}',
	project TEXT NOT NULL DEFAULT '',
	export_path TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS assets_created ON assets(created_at DESC);
CREATE TABLE IF NOT EXISTS stage_costs (
	job TEXT NOT NULL,
	stage TEXT NOT NULL,
	provider TEXT NOT NULL,
	model TEXT NOT NULL,
	amount REAL NOT NULL,
	currency TEXT NOT NULL,
	at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS stage_costs_at ON stage_costs(at);
CREATE INDEX IF NOT EXISTS stage_costs_job ON stage_costs(job);
CREATE TABLE IF NOT EXISTS license_acceptances (
	model TEXT NOT NULL,
	license TEXT NOT NULL,
	accepted_at INTEGER NOT NULL,
	PRIMARY KEY (model, license)
);
CREATE TABLE IF NOT EXISTS provider_catalog (
	provider TEXT PRIMARY KEY,
	entry TEXT NOT NULL,
	at INTEGER NOT NULL
);
`

func Open(dataDir string) (*DB, error) {
	dsn := "file:" + filepath.ToSlash(filepath.Join(dataDir, "library.db")) +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One writer avoids SQLITE_BUSY churn; WAL keeps readers concurrent.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("library schema: %w", err)
	}
	if err := addColumns(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("library schema: %w", err)
	}
	return &DB{sql: db}, nil
}

// added are columns that came after their table; libraries made before them gain them here.
var added = []struct{ table, column, ddl string }{
	{"jobs", "views", "TEXT NOT NULL DEFAULT '{}'"},
	{"jobs", "sheet", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "seed", "INTEGER NOT NULL DEFAULT 42"},
}

func addColumns(db *sql.DB) error {
	for _, c := range added {
		var n int
		err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&n)
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.column + ` ` + c.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *DB) Close() error { return d.sql.Close() }

func (d *DB) SaveJob(ctx context.Context, j *Job) error {
	overrides, _ := json.Marshal(j.Overrides)
	stages, _ := json.Marshal(j.Stages)
	views, _ := json.Marshal(j.Views)
	sheet := ""
	if j.Sheet != nil {
		raw, _ := json.Marshal(j.Sheet)
		sheet = string(raw)
	}
	var finished any
	if j.FinishedAt != nil {
		finished = j.FinishedAt.UnixMilli()
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO jobs (id, created_at, finished_at, status, name, preset, input, project, priority, overrides, stages,
			asset_id, error, views, sheet, seed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET finished_at=excluded.finished_at, status=excluded.status,
			stages=excluded.stages, asset_id=excluded.asset_id, error=excluded.error, views=excluded.views,
			sheet=excluded.sheet`,
		j.ID, j.CreatedAt.UnixMilli(), finished, j.Status, j.Name, j.Preset, j.Input, j.Project, j.Priority,
		string(overrides), string(stages), j.AssetID, j.Error, string(views), sheet, j.Seed)
	return err
}

const jobColumns = `id, created_at, finished_at, status, name, preset, input, project, priority, overrides, stages,
	asset_id, error, views, sheet, seed`

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var created int64
	var finished sql.NullInt64
	var overrides, stages, views, sheet string
	if err := row.Scan(&j.ID, &created, &finished, &j.Status, &j.Name, &j.Preset, &j.Input, &j.Project,
		&j.Priority, &overrides, &stages, &j.AssetID, &j.Error, &views, &sheet, &j.Seed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	j.CreatedAt = time.UnixMilli(created)
	if finished.Valid {
		t := time.UnixMilli(finished.Int64)
		j.FinishedAt = &t
	}
	_ = json.Unmarshal([]byte(overrides), &j.Overrides)
	_ = json.Unmarshal([]byte(stages), &j.Stages)
	_ = json.Unmarshal([]byte(views), &j.Views)
	if sheet != "" {
		j.Sheet = &Sheet{}
		_ = json.Unmarshal([]byte(sheet), j.Sheet)
	}
	return &j, nil
}

func (d *DB) Job(ctx context.Context, id string) (*Job, error) {
	return scanJob(d.sql.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
}

func (d *DB) Jobs(ctx context.Context, limit int, before time.Time) ([]*Job, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+jobColumns+` FROM jobs WHERE created_at < ? ORDER BY created_at DESC LIMIT ?`,
		before.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// FailUnfinished marks jobs interrupted by a core restart.
func (d *DB) FailUnfinished(ctx context.Context) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE jobs SET status = 'failed', error = 'interrupted: the core stopped while this job ran', finished_at = ?
		 WHERE status IN ('queued', 'running')`, time.Now().UnixMilli())
	return err
}

// CacheGet returns the outputs recorded for a stage key.
func (d *DB) CacheGet(ctx context.Context, key string) ([]Output, bool, error) {
	var raw string
	err := d.sql.QueryRowContext(ctx, `SELECT outputs FROM stage_cache WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var outs []Output
	if err := json.Unmarshal([]byte(raw), &outs); err != nil {
		return nil, false, err
	}
	_, _ = d.sql.ExecContext(ctx, `UPDATE stage_cache SET hits = hits + 1 WHERE key = ?`, key)
	return outs, true, nil
}

func (d *DB) CachePut(ctx context.Context, key string, outs []Output, seconds float64) error {
	raw, _ := json.Marshal(outs)
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO stage_cache (key, outputs, seconds, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET outputs = excluded.outputs`,
		key, string(raw), seconds, time.Now().UnixMilli())
	return err
}

// CacheForget drops a key whose artifact vanished from the object store.
func (d *DB) CacheForget(ctx context.Context, key string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM stage_cache WHERE key = ?`, key)
	return err
}

func (d *DB) RecordTiming(ctx context.Context, use, model, provider string, seconds float64) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO stage_timings (use, model, provider, seconds, at) VALUES (?, ?, ?, ?, ?)`,
		use, model, provider, seconds, time.Now().UnixMilli())
	return err
}

// EstimateSeconds is the median of the last 20 timings for a stage on a provider; 0 if unknown.
func (d *DB) EstimateSeconds(ctx context.Context, use, model, provider string) float64 {
	return d.median(ctx,
		`SELECT seconds FROM stage_timings WHERE use = ? AND model = ? AND provider = ? ORDER BY at DESC LIMIT 20`,
		use, model, provider)
}

// EstimateSecondsAnywhere is the median of the last 20 timings for a stage on any provider; 0
// if it never ran.
func (d *DB) EstimateSecondsAnywhere(ctx context.Context, use, model string) float64 {
	return d.median(ctx, `SELECT seconds FROM stage_timings WHERE use = ? AND model = ? ORDER BY at DESC LIMIT 20`, use, model)
}

func (d *DB) median(ctx context.Context, query string, args ...any) float64 {
	rows, err := d.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return 0
	}
	defer rows.Close()
	var xs []float64
	for rows.Next() {
		var s float64
		if rows.Scan(&s) == nil {
			xs = append(xs, s)
		}
	}
	if len(xs) == 0 {
		return 0
	}
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
	return xs[len(xs)/2]
}

func (d *DB) SaveAsset(ctx context.Context, a *Asset) error {
	stats, _ := json.Marshal(a.Stats)
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO assets (id, job_id, name, preset, glb, input, thumbnail, stats, project, export_path, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET thumbnail = excluded.thumbnail, stats = excluded.stats,
			export_path = excluded.export_path, name = excluded.name`,
		a.ID, a.JobID, a.Name, a.Preset, a.GLB, a.Input, a.Thumbnail, string(stats), a.Project, a.ExportPath,
		a.CreatedAt.UnixMilli())
	return err
}

const assetColumns = `id, job_id, name, preset, glb, input, thumbnail, stats, project, export_path, created_at`

func scanAsset(row interface{ Scan(...any) error }) (*Asset, error) {
	var a Asset
	var stats string
	var created int64
	if err := row.Scan(&a.ID, &a.JobID, &a.Name, &a.Preset, &a.GLB, &a.Input, &a.Thumbnail, &stats, &a.Project,
		&a.ExportPath, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(stats), &a.Stats)
	a.CreatedAt = time.UnixMilli(created)
	return &a, nil
}

func (d *DB) Asset(ctx context.Context, id string) (*Asset, error) {
	return scanAsset(d.sql.QueryRowContext(ctx, `SELECT `+assetColumns+` FROM assets WHERE id = ?`, id))
}

func (d *DB) Assets(ctx context.Context, limit int, before time.Time, query string) ([]*Asset, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+assetColumns+` FROM assets WHERE created_at < ? AND (? = '' OR name LIKE '%' || ? || '%' OR preset = ?)
		 ORDER BY created_at DESC LIMIT ?`,
		before.UnixMilli(), query, query, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAsset removes an asset and the job that made it from the library, and returns what
// it removed. The store keeps their files, which the stage cache may still use; costs stay
// in the spending totals, since they were paid.
func (d *DB) DeleteAsset(ctx context.Context, id string) (*Asset, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	a, err := scanAsset(tx.QueryRowContext(ctx, `SELECT `+assetColumns+` FROM assets WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM jobs WHERE id = ? AND status IN ('succeeded', 'failed', 'cancelled')`, a.JobID); err != nil {
		return nil, err
	}
	return a, tx.Commit()
}

// Kept lists the objects the library still shows or needs: every asset's GLB, thumbnail and
// input, every job's images, and all that an unfinished job references. Stage outputs of
// finished jobs are not among them: they are the stage cache, which eviction may trim.
func (d *DB) Kept(ctx context.Context) (map[string]bool, error) {
	kept := map[string]bool{}
	keep := func(refs ...string) {
		for _, r := range refs {
			if r != "" {
				kept[r] = true
			}
		}
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT glb, thumbnail, input FROM assets`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var glb, thumb, input string
		if err := rows.Scan(&glb, &thumb, &input); err != nil {
			rows.Close()
			return nil, err
		}
		keep(glb, thumb, input)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = d.sql.QueryContext(ctx, `SELECT `+jobColumns+` FROM jobs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		keep(j.Input)
		for _, v := range j.Views {
			keep(v)
		}
		if j.Status.Done() {
			continue
		}
		for _, s := range j.Stages {
			for _, o := range s.Outputs {
				keep(o.Hash)
			}
		}
		if j.Sheet != nil {
			keep(j.Sheet.Cutout)
		}
	}
	return kept, rows.Err()
}

// ForgetOutputs drops every stage cache entry with an output among gone, the objects
// eviction is about to remove, and returns how many it dropped. A hit on such an entry
// would hand a job outputs that no longer exist.
func (d *DB) ForgetOutputs(ctx context.Context, gone map[string]bool) (int, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT key, outputs FROM stage_cache`)
	if err != nil {
		return 0, err
	}
	var stale []string
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		var outs []Output
		_ = json.Unmarshal([]byte(raw), &outs)
		for _, o := range outs {
			if gone[o.Hash] {
				stale = append(stale, key)
				break
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, key := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM stage_cache WHERE key = ?`, key); err != nil {
			return 0, err
		}
	}
	return len(stale), tx.Commit()
}

// SaveCatalog remembers a provider's latest answer to "what do you run?".
func (d *DB) SaveCatalog(ctx context.Context, provider string, entry []byte) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO provider_catalog (provider, entry, at) VALUES (?, ?, ?)
		 ON CONFLICT(provider) DO UPDATE SET entry = excluded.entry, at = excluded.at`,
		provider, string(entry), time.Now().UnixMilli())
	return err
}

// Catalogs returns every remembered provider answer by provider id.
func (d *DB) Catalogs(ctx context.Context) (map[string][]byte, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT provider, entry FROM provider_catalog`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var id, entry string
		if err := rows.Scan(&id, &entry); err != nil {
			return nil, err
		}
		out[id] = []byte(entry)
	}
	return out, rows.Err()
}

func (d *DB) ForgetCatalog(ctx context.Context, provider string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM provider_catalog WHERE provider = ?`, provider)
	return err
}

// AcceptLicense records that the user accepted a model's license, with the time.
func (d *DB) AcceptLicense(ctx context.Context, model, license string) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO license_acceptances (model, license, accepted_at) VALUES (?, ?, ?)
		 ON CONFLICT(model, license) DO NOTHING`, model, license, time.Now().UnixMilli())
	return err
}

// Acceptances maps "model license" to when it was accepted.
func (d *DB) Acceptances(ctx context.Context) (map[string]time.Time, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT model, license, accepted_at FROM license_acceptances`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var model, license string
		var at int64
		if err := rows.Scan(&model, &license, &at); err != nil {
			return nil, err
		}
		out[model+" "+license] = time.UnixMilli(at)
	}
	return out, rows.Err()
}

// Cost is what one paid stage cost.
type Cost struct {
	Job      string  `json:"job"`
	Stage    string  `json:"stage"`
	Provider string  `json:"provider"`
	Model    string  `json:"model"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

func (d *DB) RecordCost(ctx context.Context, c Cost) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO stage_costs (job, stage, provider, model, amount, currency, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.Job, c.Stage, c.Provider, c.Model, c.Amount, c.Currency, time.Now().UnixMilli())
	return err
}

// Spent sums the cost of paid stages since a time. Hosted providers bill in USD.
func (d *DB) Spent(ctx context.Context, since time.Time) (float64, error) {
	var total sql.NullFloat64
	err := d.sql.QueryRowContext(ctx, `SELECT SUM(amount) FROM stage_costs WHERE at >= ?`, since.UnixMilli()).Scan(&total)
	return total.Float64, err
}

// SpentOn sums what one job's paid stages cost so far.
func (d *DB) SpentOn(ctx context.Context, job string) (float64, error) {
	var total sql.NullFloat64
	err := d.sql.QueryRowContext(ctx, `SELECT SUM(amount) FROM stage_costs WHERE job = ?`, job).Scan(&total)
	return total.Float64, err
}
