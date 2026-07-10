// Package migration provides the authoritative, versioned PostgreSQL schema
// migration runner used by production and integration tests.
package migration

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const statementBreak = "-- +scc StatementBreak"

var migrationNamePattern = regexp.MustCompile(`^(\d{14})_[a-z0-9_]+\.sql$`)

// Source is one immutable embedded migration and its parsed statements.
type Source struct {
	Version    int64    `json:"version"`
	Name       string   `json:"name"`
	Checksum   string   `json:"checksum"`
	Statements []string `json:"-"`
}

// LoadSources validates and loads every SQL migration from fsys.
func LoadSources(fsys fs.FS) ([]Source, error) {
	paths, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("list embedded migrations: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no embedded migrations found")
	}
	sort.Strings(paths)

	sources := make([]Source, 0, len(paths))
	seen := make(map[int64]string, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		match := migrationNamePattern.FindStringSubmatch(name)
		if match == nil {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse migration version %q: %w", name, err)
		}
		if previous, duplicate := seen[version]; duplicate {
			return nil, fmt.Errorf("duplicate migration version %d in %q and %q", version, previous, name)
		}

		raw, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		statements, err := splitStatements(string(raw))
		if err != nil {
			return nil, fmt.Errorf("parse migration %q: %w", name, err)
		}
		digest := sha256.Sum256(raw)
		sources = append(sources, Source{
			Version: version, Name: name, Checksum: hex.EncodeToString(digest[:]), Statements: statements,
		})
		seen[version] = name
	}
	return sources, nil
}

func splitStatements(script string) ([]string, error) {
	scanner := bufio.NewScanner(strings.NewReader(script))
	// Migrations can contain long generated SQL lines; keep the scanner limit
	// comfortably above the repository's formatting policy.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var current strings.Builder
	statements := make([]string, 0, 8)
	flush := func() {
		statement := strings.TrimSpace(current.String())
		current.Reset()
		if hasExecutableSQL(statement) {
			statements = append(statements, statement)
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == statementBreak {
			flush()
			continue
		}
		current.WriteString(line)
		current.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan SQL: %w", err)
	}
	flush()
	if len(statements) == 0 {
		return nil, fmt.Errorf("migration has no executable SQL")
	}
	return statements, nil
}

func hasExecutableSQL(statement string) bool {
	for _, line := range strings.Split(statement, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			return true
		}
	}
	return false
}
