package store

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Every Store passes the same tests: Memory always, Postgres when VMSERVICE_TEST_DATABASE_URL names
// a scratch database (its tables are dropped first).
func stores(t *testing.T) map[string]Store {
	out := map[string]Store{"memory": NewMemory()}
	url := os.Getenv("VMSERVICE_TEST_DATABASE_URL")
	if url == "" {
		return out
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `DROP TABLE IF EXISTS ssh_keys, machines, members, schema_migrations`); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	p, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	// Opening twice applies nothing twice.
	again, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	again.Close()
	out["postgres"] = p
	return out
}

func limit2(u Usage) error {
	if u.VMs >= 2 {
		return &LimitError{"over"}
	}
	return nil
}

func TestMachines(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s.UpsertMember(ctx, "alice", "alice")
			s.UpsertMember(ctx, "bob", "bob")
			m := Machine{ID: "vm-aaaaaa", Owner: "alice", Name: "box", Image: "ubuntu", Size: "small", CPU: 2, MemoryMiB: 2048, DiskGiB: 20}
			if err := s.CreateMachine(ctx, m, limit2); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateMachine(ctx, m, limit2); !errors.Is(err, ErrExists) {
				t.Errorf("same id again = %v, want ErrExists", err)
			}
			got, err := s.Machine(ctx, "vm-aaaaaa")
			if err != nil || got.Owner != "alice" || got.CPU != 2 || got.CreatedAt.IsZero() {
				t.Errorf("machine = %+v, %v", got, err)
			}
			if _, err := s.Machine(ctx, "vm-zzzzzz"); !errors.Is(err, ErrNotFound) {
				t.Errorf("missing = %v", err)
			}
			if u, _ := s.Usage(ctx, "alice"); u != (Usage{VMs: 1, CPU: 2, MemoryMiB: 2048, DiskGiB: 20}) {
				t.Errorf("usage = %+v", u)
			}
			if named, _ := s.MachinesNamed(ctx, "alice", "BOX"); len(named) != 1 || named[0].ID != "vm-aaaaaa" {
				t.Errorf("named BOX = %+v", named)
			}
			if named, _ := s.MachinesNamed(ctx, "bob", "box"); len(named) != 0 {
				t.Errorf("bob finds alice's box: %+v", named)
			}
			if list, _ := s.Machines(ctx, "bob"); len(list) != 0 {
				t.Errorf("bob sees %v", list)
			}
			if err := s.CreateMachine(ctx, Machine{ID: "vm-bbbbbb", Owner: "nobody", Name: "x"}, limit2); err == nil {
				t.Error("a machine for a non-member was created")
			}
			s.DeleteMachine(ctx, "vm-aaaaaa")
			if _, err := s.Machine(ctx, "vm-aaaaaa"); !errors.Is(err, ErrNotFound) {
				t.Errorf("after delete = %v", err)
			}
		})
	}
}

// Twenty parallel creates against a limit of two: exactly two rows.
func TestLimitHoldsUnderConcurrency(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s.UpsertMember(ctx, "carol", "carol")
			var wg sync.WaitGroup
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					s.CreateMachine(ctx, Machine{ID: fmt.Sprintf("vm-c%05d", i), Owner: "carol", Name: "x", CPU: 1, MemoryMiB: 1, DiskGiB: 1}, limit2)
				}()
			}
			wg.Wait()
			if u, _ := s.Usage(ctx, "carol"); u.VMs != 2 {
				t.Errorf("20 parallel creates made %d machines; the limit is 2", u.VMs)
			}
		})
	}
}

func TestKeys(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s.UpsertMember(ctx, "alice", "alice")
			s.UpsertMember(ctx, "bob", "bob")
			k := Key{ID: "k1", Owner: "alice", Line: "ssh-ed25519 AAAA", Label: "laptop"}
			if err := s.AddKey(ctx, k, 2); err != nil {
				t.Fatal(err)
			}
			if err := s.AddKey(ctx, Key{ID: "k1", Owner: "bob", Line: "ssh-ed25519 AAAA"}, 2); !errors.Is(err, ErrExists) {
				t.Errorf("bob adding alice's key = %v, want ErrExists", err)
			}
			s.AddKey(ctx, Key{ID: "k2", Owner: "alice", Line: "x"}, 2)
			var le *LimitError
			if err := s.AddKey(ctx, Key{ID: "k3", Owner: "alice", Line: "y"}, 2); !errors.As(err, &le) {
				t.Errorf("third key = %v, want LimitError", err)
			}
			if got, err := s.Key(ctx, "k1"); err != nil || got.Owner != "alice" || got.Label != "laptop" {
				t.Errorf("key = %+v, %v", got, err)
			}
			if err := s.DeleteKey(ctx, "bob", "k1"); !errors.Is(err, ErrNotFound) {
				t.Errorf("bob deleting alice's key = %v", err)
			}
			if err := s.DeleteKey(ctx, "alice", "k1"); err != nil {
				t.Errorf("alice delete = %v", err)
			}
			if keys, _ := s.Keys(ctx, "alice"); len(keys) != 1 || keys[0].ID != "k2" {
				t.Errorf("keys = %+v", keys)
			}
		})
	}
}

// The bastion's role reads members, machines and keys, and can change nothing.
func TestReadOnlyRole(t *testing.T) {
	url := os.Getenv("VMSERVICE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("needs VMSERVICE_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	stores(t) // fresh schema
	owner, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	owner.pool.Exec(ctx, `DROP ROLE IF EXISTS bastion_test`)
	if _, err := owner.pool.Exec(ctx, `CREATE ROLE bastion_test LOGIN PASSWORD 'pw'`); err != nil {
		t.Fatal(err)
	}
	if err := owner.GrantRead(ctx, "bastion_test"); err != nil {
		t.Fatal(err)
	}
	owner.UpsertMember(ctx, "alice", "alice")
	owner.AddKey(ctx, Key{ID: "k1", Owner: "alice", Line: "ssh-ed25519 AAAA"}, 20)
	u, _ := neturl.Parse(url)
	u.User = neturl.UserPassword("bastion_test", "pw")
	ro, err := OpenReadOnly(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if k, err := ro.Key(ctx, "k1"); err != nil || k.Owner != "alice" {
		t.Errorf("read as bastion = %+v, %v", k, err)
	}
	if _, err := ro.SignedInAt(ctx, "alice"); err != nil {
		t.Errorf("sign-in time as bastion: %v", err)
	}
	if err := ro.AddKey(ctx, Key{ID: "k2", Owner: "alice", Line: "x"}, 20); err == nil {
		t.Error("the bastion's role could add a key")
	}
	if err := ro.DeleteKey(ctx, "alice", "k1"); err == nil {
		t.Error("the bastion's role could delete a key")
	}
}
