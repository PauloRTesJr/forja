package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Edit changes keys in one config file. It edits line by line, so comments and layout
// survive; a file it cannot edit that way is rewritten whole, with the original kept as
// <file>.bak. Keys are dotted paths ("engine.fit_mode", "models.stages.shape"); a nil value
// removes the key. validate runs on the result (normally a full Load); when it fails, the
// file goes back to how it was.
func Edit(path string, changes map[string]any, validate func() error) error {
	return rewrite(path, func(want map[string]any) {
		for k, v := range changes {
			setPath(want, strings.Split(k, "."), v)
		}
	}, func(text string) (string, bool) { return editLines(text, changes) }, validate)
}

// AddProvider appends a [[providers]] table to a config file, keeping the rest of it.
func AddProvider(path string, p Provider, validate func() error) error {
	fields := providerFields(p)
	return rewrite(path, func(want map[string]any) {
		list, _ := want["providers"].([]map[string]any)
		want["providers"] = append(list, fields)
	}, func(text string) (string, bool) {
		var b strings.Builder
		b.WriteString(strings.TrimRight(text, "\n"))
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("[[providers]]\n")
		for _, k := range []string{"id", "label", "kind", "url", "secret", "fingerprint"} {
			if v, ok := fields[k]; ok {
				val, _ := encodeValue(v)
				b.WriteString(k + " = " + val + "\n")
			}
		}
		return b.String(), true
	}, validate)
}

// RemoveProvider deletes the [[providers]] table with this id from a config file.
func RemoveProvider(path, id string, validate func() error) error {
	return rewrite(path, func(want map[string]any) {
		list, _ := want["providers"].([]map[string]any)
		kept := []map[string]any{}
		for _, p := range list {
			if p["id"] != id {
				kept = append(kept, p)
			}
		}
		if len(kept) == 0 {
			delete(want, "providers")
		} else {
			want["providers"] = kept
		}
	}, func(text string) (string, bool) { return removeProviderLines(text, id) }, validate)
}

func providerFields(p Provider) map[string]any {
	out := map[string]any{"id": p.ID, "kind": p.Kind}
	for k, v := range map[string]string{"label": p.Label, "url": p.URL, "secret": p.Secret, "fingerprint": p.Fingerprint} {
		if v != "" {
			out[k] = v
		}
	}
	if p.Paid {
		out["paid"] = true
	}
	return out
}

// removeProviderLines drops the lines of one [[providers]] table, up to the next header.
func removeProviderLines(text, id string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start, end := -1, len(lines)
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "[[providers]]" {
			continue
		}
		j := i + 1
		match := false
		for ; j < len(lines); j++ {
			if headerLine.MatchString(lines[j]) || arrayLine.MatchString(lines[j]) {
				break
			}
			if m := keyLine.FindStringSubmatch(lines[j]); m != nil && unquote(m[1]) == "id" {
				var probe map[string]any
				if _, err := toml.Decode(lines[j], &probe); err == nil && probe["id"] == id {
					match = true
				}
			}
		}
		if match {
			start, end = i, j
			break
		}
		i = j - 1
	}
	if start < 0 {
		return text, true
	}
	if start > 0 && strings.TrimSpace(lines[start-1]) == "" {
		start--
	}
	return strings.Join(append(lines[:start], lines[end:]...), "\n"), true
}

// rewrite changes one config file: change describes the result as data, lineEdit tries to
// produce it as text. When the text decodes to something else, the file is encoded anew.
func rewrite(path string, change func(map[string]any), lineEdit func(string) (string, bool), validate func() error) error {
	before, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	current := map[string]any{}
	if _, err := toml.Decode(string(before), &current); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	want := deepCopy(current)
	change(want)
	after, ok := lineEdit(string(before))
	if ok {
		got := map[string]any{}
		_, err := toml.Decode(after, &got)
		ok = err == nil && reflect.DeepEqual(normalize(got), normalize(want))
	}
	if !ok {
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(want); err != nil {
			return err
		}
		after = buf.String()
		if len(before) > 0 {
			if err := os.WriteFile(path+".bak", before, 0o600); err != nil {
				return err
			}
		}
	}
	if err := writeAtomic(path, []byte(after)); err != nil {
		return err
	}
	if validate != nil {
		if err := validate(); err != nil {
			if len(before) == 0 {
				_ = os.Remove(path)
			} else {
				_ = writeAtomic(path, before)
			}
			return err
		}
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".forja-*.toml")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

var (
	headerLine = regexp.MustCompile(`^\s*\[([^\[\]]+)\]\s*(#.*)?$`)
	arrayLine  = regexp.MustCompile(`^\s*\[\[`)
	keyLine    = regexp.MustCompile(`^\s*("[^"]*"|'[^']*'|[A-Za-z0-9_-]+)\s*=(.*)$`)
	bareKey    = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

type tomlLine struct {
	text  string
	table string // dotted path of the table the line is in; "" before any header
	key   string // the key a key line sets
	head  bool
	array bool // inside an array of tables, which this editor leaves alone
}

// editLines applies changes to the text; ok is false where it cannot edit safely.
func editLines(text string, changes map[string]any) (string, bool) {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}
	lines := make([]tomlLine, 0, len(raw))
	table, array := "", false
	for _, l := range raw {
		tl := tomlLine{text: l}
		switch {
		case arrayLine.MatchString(l):
			array, table = true, ""
			tl.head = true
		case headerLine.MatchString(l):
			path, ok := splitKey(headerLine.FindStringSubmatch(l)[1])
			if !ok {
				return "", false
			}
			array, table = false, strings.Join(path, ".")
			tl.head = true
		default:
			if m := keyLine.FindStringSubmatch(l); m != nil {
				val := strings.TrimSpace(m[2])
				// Multi-line values continue on lines this editor would misread.
				if strings.HasPrefix(val, `"""`) || strings.HasPrefix(val, "'''") ||
					(strings.HasPrefix(val, "[") && strings.Count(val, "[") != strings.Count(val, "]")) ||
					(strings.HasPrefix(val, "{") && !strings.Contains(val, "}")) {
					return "", false
				}
				tl.key = unquote(m[1])
			}
		}
		tl.table, tl.array = table, array
		lines = append(lines, tl)
	}

	for k, v := range changes {
		parts := strings.Split(k, ".")
		tbl, leaf := strings.Join(parts[:len(parts)-1], "."), parts[len(parts)-1]
		if _, isMap := v.(map[string]any); isMap {
			return "", false
		}
		found := -1
		for i, l := range lines {
			if !l.array && !l.head && l.table == tbl && l.key == leaf {
				found = i
			}
		}
		if v == nil {
			if found >= 0 {
				lines = append(lines[:found], lines[found+1:]...)
			}
			continue
		}
		value, ok := encodeValue(v)
		if !ok {
			return "", false
		}
		entry := tomlLine{text: quoteKey(leaf) + " = " + value, table: tbl, key: leaf}
		switch {
		case found >= 0:
			indent := lines[found].text[:len(lines[found].text)-len(strings.TrimLeft(lines[found].text, " \t"))]
			entry.text = indent + entry.text
			lines[found] = entry
		default:
			lines = insertKey(lines, entry)
		}
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.text)
		b.WriteByte('\n')
	}
	return b.String(), true
}

// insertKey puts a new key at the end of its table's keys, creating the table when needed.
func insertKey(lines []tomlLine, entry tomlLine) []tomlLine {
	at := -1
	for i, l := range lines {
		if l.array || l.table != entry.table {
			continue
		}
		if (l.head && entry.table != "") || l.key != "" {
			at = i
		}
	}
	if at < 0 && entry.table == "" {
		// Top-level keys go before the first table.
		for i, l := range lines {
			if l.head {
				return append(lines[:i], append([]tomlLine{entry, {}}, lines[i:]...)...)
			}
		}
		return append(lines, entry)
	}
	if at < 0 {
		if n := len(lines); n > 0 && strings.TrimSpace(lines[n-1].text) != "" {
			lines = append(lines, tomlLine{})
		}
		head := tomlLine{text: "[" + joinKey(strings.Split(entry.table, ".")) + "]", table: entry.table, head: true}
		return append(lines, head, entry)
	}
	return append(lines[:at+1], append([]tomlLine{entry}, lines[at+1:]...)...)
}

func encodeValue(v any) (string, bool) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(map[string]any{"v": v}); err != nil {
		return "", false
	}
	out, ok := strings.CutPrefix(strings.TrimSpace(buf.String()), "v = ")
	return out, ok && !strings.Contains(out, "\n")
}

// splitKey splits a dotted key with optional quoted segments.
func splitKey(s string) ([]string, bool) {
	var out []string
	for s = strings.TrimSpace(s); s != ""; {
		var seg string
		switch s[0] {
		case '"', '\'':
			end := strings.IndexByte(s[1:], s[0])
			if end < 0 {
				return nil, false
			}
			seg, s = s[1:end+1], s[end+2:]
		default:
			end := strings.IndexByte(s, '.')
			if end < 0 {
				end = len(s)
			}
			seg, s = strings.TrimSpace(s[:end]), s[end:]
		}
		out = append(out, seg)
		s = strings.TrimSpace(s)
		if s != "" {
			if s[0] != '.' {
				return nil, false
			}
			s = strings.TrimSpace(s[1:])
		}
	}
	return out, len(out) > 0
}

func unquote(k string) string {
	if len(k) >= 2 && (k[0] == '"' || k[0] == '\'') {
		return k[1 : len(k)-1]
	}
	return k
}

func quoteKey(k string) string {
	if bareKey.MatchString(k) {
		return k
	}
	return `"` + strings.ReplaceAll(strings.ReplaceAll(k, `\`, `\\`), `"`, `\"`) + `"`
}

func joinKey(parts []string) string {
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = quoteKey(p)
	}
	return strings.Join(q, ".")
}

func setPath(m map[string]any, path []string, v any) {
	for _, p := range path[:len(path)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	if v == nil {
		delete(m, path[len(path)-1])
		return
	}
	m[path[len(path)-1]] = v
}

func deepCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			v = deepCopy(sub)
		}
		out[k] = v
	}
	return out
}

// normalize makes decoded and in-memory trees comparable: numbers as float64, slices as []any,
// and empty tables dropped (the line editor may leave a header whose keys were removed).
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			n := normalize(val)
			if sub, ok := n.(map[string]any); ok && len(sub) == 0 {
				continue
			}
			out[k] = n
		}
		return out
	case []map[string]any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = e
		}
		return out
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float32:
		return float64(x)
	}
	return v
}
