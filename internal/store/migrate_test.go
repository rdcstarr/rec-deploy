package store

import (
	"io/fs"
	"strings"
	"testing"
)

// TestMigrationsAreAdditive defends the invariant that makes self-update's
// rollback able to restore service rather than merely restore a file.
//
// migrate applies only the migrations the running binary embeds, and skips by
// recorded version; nothing reads MAX(version). So a v1 binary opening a database
// a v2 binary already migrated simply works — but only for as long as every
// migration is additive. That holds today by discipline, not by rule, and the one
// path that depends on it is the least watched: after a bad release, the v1 binary
// self-update restored writes RecordBadTag into update_state. If a migration ever
// restructures that table, the bad tag is never recorded and the bad release is
// reinstalled and rolled back every hour, forever.
//
// ADD COLUMN ... NOT NULL without a DEFAULT is the trap worth naming, because it
// is data-dependent and therefore differs server by server. Verified against
// modernc.org/sqlite: on a table with rows the ALTER fails loudly, so it looks
// like a migration bug and gets fixed; on an empty table it SUCCEEDS, and then a
// v1 INSERT that omits the column fails with "NOT NULL constraint failed". The
// same file is a loud failure on a busy box and a silent v1-breaker on a fresh
// one. update_state escapes this shape only because 0002_update_state.sql seeds a
// row — an accident, not a plan.
//
// What this test cannot see: a new UNIQUE index or trigger over a table an
// earlier migration created, which would also reject a v1 write. CREATE UNIQUE
// INDEX greps perfectly well; what needs a parser is telling "index over the
// table this migration just created" — 0001_init.sql does exactly that, legally —
// from "index over a table that already existed". The Vanilla Code Law says do not
// write that parser, so this is the one shape a human has to catch in review.
func TestMigrationsAreAdditive(t *testing.T) {
	// Each rule is a substring match over normalised SQL. Statements are matched,
	// not parsed: the point is to make a dangerous migration hard to write by
	// accident, not to be a SQLite frontend.
	banned := []struct {
		needle string
		why    string
	}{
		{"drop table", "an older binary still selects from it"},
		{"drop column", "an older binary still names it"},
		{"rename to", "an older binary still names the old table"},
		{"rename column", "an older binary still names the old column"},
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no migrations found — this test would pass vacuously")
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}

		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}

		for _, stmt := range statements(string(body)) {
			for _, b := range banned {
				if strings.Contains(stmt, b.needle) {
					t.Errorf("%s: %q is not additive — %s.\nA binary that predates this migration must keep working against the migrated database: that is what lets a rollback restore service.", e.Name(), b.needle, b.why)
				}
			}

			// ADD COLUMN is additive only with a way to fill the existing rows.
			if strings.Contains(stmt, "add column") && strings.Contains(stmt, "not null") && !hasDefaultClause(stmt) {
				t.Errorf("%s: ADD COLUMN ... NOT NULL without a DEFAULT.\nOn an empty table this succeeds and then rejects every INSERT from a binary that does not know the column; on a populated one it fails outright. Give it a DEFAULT.\n  %s", e.Name(), stmt)
			}
		}
	}
}

// hasDefaultClause reports whether stmt carries a DEFAULT clause, as a word.
//
// A substring search finds the wrong thing: `default_branch` is an ordinary
// column name for a repo table in a deploy tool, and it satisfies "default"
// while the statement has no DEFAULT clause at all — so the very migration this
// rule exists to stop would sail through it. The clause is a token; match it as
// one.
func hasDefaultClause(stmt string) bool {
	for _, f := range strings.Fields(stmt) {
		// `DEFAULT ''` and `DEFAULT(datetime('now'))` are both legal.
		if f == "default" || strings.HasPrefix(f, "default(") {
			return true
		}
	}

	return false
}

// statements splits a migration into lowercased statements with comments and
// newlines flattened, so a rule can match a statement that was formatted across
// several lines.
func statements(sql string) []string {
	var b strings.Builder
	for line := range strings.SplitSeq(sql, "\n") {
		if before, _, found := strings.Cut(line, "--"); found {
			line = before
		}
		b.WriteString(line)
		b.WriteString(" ")
	}

	var out []string
	for stmt := range strings.SplitSeq(b.String(), ";") {
		stmt = strings.ToLower(strings.Join(strings.Fields(stmt), " "))
		if stmt != "" {
			out = append(out, stmt)
		}
	}

	return out
}
