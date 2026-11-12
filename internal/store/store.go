// Package store is VMService's own data in Postgres: who the members are, which machines are whose
// (rights), and their SSH keys. What a machine is and does lives in its KubeVirt object; this says
// whose it is.
//
// Checks that must hold however many requests arrive at once (per-member limits) run in
// SERIALIZABLE transactions: the database refuses one of two conflicting ones and it's retried.
// Uniqueness (a key belongs to one member) is a primary key.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Machine is a machine's row: its owner and what it was made with.
type Machine struct {
	ID        string
	Owner     string
	Name      string
	Image     string
	Size      string
	CPU       int
	MemoryMiB int
	DiskGiB   int
	CreatedAt time.Time
}

// Key is an SSH public key.
type Key struct {
	ID        string
	Owner     string
	Line      string
	Label     string
	CreatedAt time.Time
}

// Usage is what an owner's machines add up to.
type Usage struct {
	VMs       int `json:"vms"`
	CPU       int `json:"cpu"`
	MemoryMiB int `json:"memoryMiB"`
	DiskGiB   int `json:"diskGiB"`
}

var (
	// ErrNotFound: no such row (or not the owner's, where an owner is given).
	ErrNotFound = errors.New("not found")
	// ErrExists: the primary key is taken.
	ErrExists = errors.New("already exists")
)

// LimitError is a create refused because it would go over a limit.
type LimitError struct{ Message string }

func (e *LimitError) Error() string { return e.Message }

// Store is the database.
type Store interface {
	// UpsertMember records a sign-in: the username and now as the sign-in time.
	UpsertMember(ctx context.Context, owner, username string) error
	// EnsureMember adds the member if missing (a session from before they had a row), as signed in now.
	EnsureMember(ctx context.Context, owner, username string) error
	// SignedInAt is the member's last sign-in.
	SignedInAt(ctx context.Context, owner string) (time.Time, error)
	// CreateMachine inserts m if the owner's machines plus m stay within check's limits; check
	// returns a *LimitError when they wouldn't.
	CreateMachine(ctx context.Context, m Machine, check func(Usage) error) error
	Machine(ctx context.Context, id string) (Machine, error)
	Machines(ctx context.Context, owner string) ([]Machine, error)
	// MachinesNamed is the owner's machines whose display name is name, ignoring case.
	MachinesNamed(ctx context.Context, owner, name string) ([]Machine, error)
	Usage(ctx context.Context, owner string) (Usage, error)
	DeleteMachine(ctx context.Context, id string) error
	// AddKey inserts k if the owner has fewer than max keys; ErrExists if anyone has it.
	AddKey(ctx context.Context, k Key, max int) error
	Key(ctx context.Context, id string) (Key, error)
	Keys(ctx context.Context, owner string) ([]Key, error)
	DeleteKey(ctx context.Context, owner, id string) error
}

//go:embed migrations/*.sql
var migrations embed.FS

// Postgres is the Store on a Postgres database.
type Postgres struct{ pool *pgxpool.Pool }

// Open connects and brings the schema up to date.
func Open(ctx context.Context, url string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	p := &Postgres{pool}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrating the database: %w", err)
	}
	return p, nil
}

// OpenReadOnly connects without touching the schema, for a process whose role may only read
// (the bastion).
func OpenReadOnly(ctx context.Context, url string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Postgres{pool}, nil
}

// GrantRead lets role read what the bastion needs (members, machines, ssh_keys) and nothing else.
// Run by the owner at every start, so it holds whichever was created first.
func (p *Postgres) GrantRead(ctx context.Context, role string) error {
	_, err := p.pool.Exec(ctx, `GRANT SELECT ON members, machines, ssh_keys TO `+pgx.Identifier{role}.Sanitize())
	return err
}

// Close closes the pool.
func (p *Postgres) Close() { p.pool.Close() }

// migrate applies migrations/NNN_*.sql in order, each once, in its own transaction. A concurrent
// starter applying the same version fails on the primary key and its transaction rolls back.
func (p *Postgres) migrate(ctx context.Context) error {
	if _, err := p.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, version)
			if err != nil || tag.RowsAffected() == 0 {
				return err // applied already
			}
			_, err = tx.Exec(ctx, string(sql))
			return err
		})
		if err != nil {
			return fmt.Errorf("%s: %w", version, err)
		}
	}
	return nil
}

// serializable runs fn in a SERIALIZABLE transaction, again when the database refuses it for a
// conflict with a concurrent one (SQLSTATE 40001).
func (p *Postgres) serializable(ctx context.Context, fn func(pgx.Tx) error) error {
	for attempt := 0; ; attempt++ {
		err := pgx.BeginTxFunc(ctx, p.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, fn)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "40001" && attempt < 10 {
			continue
		}
		return err
	}
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func (p *Postgres) UpsertMember(ctx context.Context, owner, username string) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO members (owner, username) VALUES ($1, $2)
		ON CONFLICT (owner) DO UPDATE SET username = EXCLUDED.username, signed_in_at = now()`, owner, username)
	return err
}

func (p *Postgres) EnsureMember(ctx context.Context, owner, username string) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO members (owner, username) VALUES ($1, $2) ON CONFLICT (owner) DO NOTHING`, owner, username)
	return err
}

func (p *Postgres) SignedInAt(ctx context.Context, owner string) (time.Time, error) {
	var t time.Time
	err := p.pool.QueryRow(ctx, `SELECT signed_in_at FROM members WHERE owner = $1`, owner).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

const usageQuery = `SELECT count(*), coalesce(sum(cpu), 0), coalesce(sum(memory_mib), 0), coalesce(sum(disk_gib), 0) FROM machines WHERE owner = $1`

func scanUsage(row pgx.Row) (Usage, error) {
	var u Usage
	err := row.Scan(&u.VMs, &u.CPU, &u.MemoryMiB, &u.DiskGiB)
	return u, err
}

func (p *Postgres) CreateMachine(ctx context.Context, m Machine, check func(Usage) error) error {
	return p.serializable(ctx, func(tx pgx.Tx) error {
		u, err := scanUsage(tx.QueryRow(ctx, usageQuery, m.Owner))
		if err != nil {
			return err
		}
		if err := check(u); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO machines (id, owner, name, image, size, cpu, memory_mib, disk_gib) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			m.ID, m.Owner, m.Name, m.Image, m.Size, m.CPU, m.MemoryMiB, m.DiskGiB)
		if isUniqueViolation(err) {
			return ErrExists
		}
		return err
	})
}

const machineColumns = `id, owner, name, image, size, cpu, memory_mib, disk_gib, created_at`

func scanMachine(row pgx.Row) (Machine, error) {
	var m Machine
	err := row.Scan(&m.ID, &m.Owner, &m.Name, &m.Image, &m.Size, &m.CPU, &m.MemoryMiB, &m.DiskGiB, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

func (p *Postgres) Machine(ctx context.Context, id string) (Machine, error) {
	return scanMachine(p.pool.QueryRow(ctx, `SELECT `+machineColumns+` FROM machines WHERE id = $1`, id))
}

func (p *Postgres) Machines(ctx context.Context, owner string) ([]Machine, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+machineColumns+` FROM machines WHERE owner = $1 ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Machine, error) { return scanMachine(r) })
}

func (p *Postgres) MachinesNamed(ctx context.Context, owner, name string) ([]Machine, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+machineColumns+` FROM machines WHERE owner = $1 AND lower(name) = lower($2) ORDER BY created_at DESC`, owner, name)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Machine, error) { return scanMachine(r) })
}

func (p *Postgres) Usage(ctx context.Context, owner string) (Usage, error) {
	return scanUsage(p.pool.QueryRow(ctx, usageQuery, owner))
}

func (p *Postgres) DeleteMachine(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM machines WHERE id = $1`, id)
	return err
}

func (p *Postgres) AddKey(ctx context.Context, k Key, max int) error {
	return p.serializable(ctx, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ssh_keys WHERE owner = $1`, k.Owner).Scan(&n); err != nil {
			return err
		}
		if n >= max {
			return &LimitError{fmt.Sprintf("You already have %d keys.", max)}
		}
		_, err := tx.Exec(ctx, `INSERT INTO ssh_keys (id, owner, key, label) VALUES ($1, $2, $3, $4)`, k.ID, k.Owner, k.Line, k.Label)
		if isUniqueViolation(err) {
			return ErrExists
		}
		return err
	})
}

const keyColumns = `id, owner, key, label, created_at`

func scanKey(row pgx.Row) (Key, error) {
	var k Key
	err := row.Scan(&k.ID, &k.Owner, &k.Line, &k.Label, &k.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

func (p *Postgres) Key(ctx context.Context, id string) (Key, error) {
	return scanKey(p.pool.QueryRow(ctx, `SELECT `+keyColumns+` FROM ssh_keys WHERE id = $1`, id))
}

func (p *Postgres) Keys(ctx context.Context, owner string) ([]Key, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+keyColumns+` FROM ssh_keys WHERE owner = $1 ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Key, error) { return scanKey(r) })
}

func (p *Postgres) DeleteKey(ctx context.Context, owner, id string) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM ssh_keys WHERE id = $1 AND owner = $2`, id, owner)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
