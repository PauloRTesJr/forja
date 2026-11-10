package library

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// The orders a list of assets comes in.
const (
	SortNewest = "newest"
	SortOldest = "oldest"
	SortName   = "name"
)

// AssetQuery is one page of the library's assets.
type AssetQuery struct {
	// Limit is the size of the page and Offset where it starts among the matching assets.
	Limit, Offset int
	// Before keeps assets created earlier; the zero time keeps all.
	Before time.Time
	// Sort is SortNewest (also ""), SortOldest or SortName.
	Sort string
	// Match keeps the assets it accepts; nil keeps all.
	Match func(*Asset) bool
}

// FindAssets returns a page of the assets that match q, and how many match in all.
func (d *DB) FindAssets(ctx context.Context, q AssetQuery) ([]*Asset, int, error) {
	order, ok := map[string]string{
		"": "created_at DESC, id DESC", SortNewest: "created_at DESC, id DESC", SortOldest: "created_at, id",
		// Sorted below: SQLite folds the case of ASCII letters only.
		SortName: "created_at DESC, id DESC",
	}[q.Sort]
	if !ok {
		return nil, 0, fmt.Errorf("sort %q: want %s, %s or %s", q.Sort, SortNewest, SortOldest, SortName)
	}
	before := int64(math.MaxInt64)
	if !q.Before.IsZero() {
		before = q.Before.UnixMilli()
	}
	inSQL := q.Match == nil && q.Sort != SortName
	query, args := `SELECT `+assetColumns+` FROM assets WHERE created_at < ? ORDER BY `+order, []any{before}
	total := 0
	if inSQL {
		if err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets WHERE created_at < ?`, before).Scan(&total); err != nil {
			return nil, 0, err
		}
		query, args = query+` LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)
	}
	rows, err := d.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, 0, err
		}
		if q.Match == nil || q.Match(a) {
			out = append(out, a)
		}
	}
	if err := rows.Err(); err != nil || inSQL {
		return out, total, err
	}
	if q.Sort == SortName {
		sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	}
	total = len(out)
	out = out[min(q.Offset, total):]
	return out[:min(q.Limit, len(out))], total, nil
}

// Assets lists the newest assets created before a time.
func (d *DB) Assets(ctx context.Context, limit int, before time.Time) ([]*Asset, error) {
	list, _, err := d.FindAssets(ctx, AssetQuery{Limit: limit, Before: before})
	return list, err
}

// MaxName is the longest name an asset may be given, in characters.
const MaxName = 120

// CheckName says why a name will not do for an asset, in the user's terms, or returns it
// trimmed.
func CheckName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", fmt.Errorf("give the asset a name")
	case utf8.RuneCountInString(name) > MaxName:
		return "", fmt.Errorf("the name is longer than %d characters", MaxName)
	case strings.ContainsFunc(name, func(r rune) bool { return r < 32 || r == 127 }):
		return "", fmt.Errorf("the name cannot hold line breaks or control characters")
	}
	return name, nil
}

// RenameAsset gives an asset another name and returns it.
func (d *DB) RenameAsset(ctx context.Context, id, name string) (*Asset, error) {
	a, err := d.Asset(ctx, id)
	if err != nil {
		return nil, err
	}
	a.Name = name
	return a, d.SaveAsset(ctx, a)
}
