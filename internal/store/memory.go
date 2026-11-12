package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Memory is a Store in memory, for tests. It keeps the same rules as Postgres (owner rows must
// exist, keys are unique, limits are checked atomically).
type Memory struct {
	mu       sync.Mutex
	members  map[string]string
	signedIn map[string]time.Time
	machines map[string]Machine
	keys     map[string]Key
	media    map[string]Medium
	clock    time.Time
}

// NewMemory is an empty Memory store.
func NewMemory() *Memory {
	return &Memory{members: map[string]string{}, signedIn: map[string]time.Time{}, machines: map[string]Machine{}, keys: map[string]Key{}, media: map[string]Medium{}, clock: time.Unix(1_800_000_000, 0)}
}

func (m *Memory) now() time.Time { m.clock = m.clock.Add(time.Second); return m.clock }

func (m *Memory) UpsertMember(_ context.Context, owner, username string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.members[owner] = username
	m.signedIn[owner] = time.Now()
	return nil
}

func (m *Memory) EnsureMember(_ context.Context, owner, username string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.members[owner]; !ok {
		m.members[owner] = username
		m.signedIn[owner] = time.Now()
	}
	return nil
}

func (m *Memory) SignedInAt(_ context.Context, owner string) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.signedIn[owner]
	if !ok {
		return t, ErrNotFound
	}
	return t, nil
}

// SetSignedIn is for tests: pretend owner last signed in at t.
func (m *Memory) SetSignedIn(owner string, t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signedIn[owner] = t
}

func (m *Memory) usage(owner string) Usage {
	var u Usage
	for _, x := range m.machines {
		if x.Owner == owner {
			u.VMs++
			u.CPU += x.CPU
			u.MemoryMiB += x.MemoryMiB
			u.DiskGiB += x.DiskGiB
		}
	}
	return u
}

func (m *Memory) CreateMachine(_ context.Context, x Machine, check func(Usage) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.members[x.Owner]; !ok {
		return fmt.Errorf("owner %q isn't a member (foreign key)", x.Owner)
	}
	if err := check(m.usage(x.Owner)); err != nil {
		return err
	}
	if _, ok := m.machines[x.ID]; ok {
		return ErrExists
	}
	x.CreatedAt = m.now()
	m.machines[x.ID] = x
	return nil
}

func (m *Memory) Machine(_ context.Context, id string) (Machine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	x, ok := m.machines[id]
	if !ok {
		return Machine{}, ErrNotFound
	}
	return x, nil
}

func (m *Memory) Machines(_ context.Context, owner string) ([]Machine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Machine
	for _, x := range m.machines {
		if x.Owner == owner {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) MachinesNamed(ctx context.Context, owner, name string) ([]Machine, error) {
	all, _ := m.Machines(ctx, owner)
	var out []Machine
	for _, x := range all {
		if strings.EqualFold(x.Name, name) {
			out = append(out, x)
		}
	}
	return out, nil
}

func (m *Memory) Usage(_ context.Context, owner string) (Usage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usage(owner), nil
}

func (m *Memory) DeleteMachine(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.machines, id)
	return nil
}

func (m *Memory) AddKey(_ context.Context, k Key, max int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.members[k.Owner]; !ok {
		return fmt.Errorf("owner %q isn't a member (foreign key)", k.Owner)
	}
	n := 0
	for _, x := range m.keys {
		if x.Owner == k.Owner {
			n++
		}
	}
	if n >= max {
		return &LimitError{fmt.Sprintf("You already have %d keys.", max)}
	}
	if _, ok := m.keys[k.ID]; ok {
		return ErrExists
	}
	k.CreatedAt = m.now()
	m.keys[k.ID] = k
	return nil
}

func (m *Memory) Key(_ context.Context, id string) (Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[id]
	if !ok {
		return Key{}, ErrNotFound
	}
	return k, nil
}

func (m *Memory) Keys(_ context.Context, owner string) ([]Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Key
	for _, k := range m.keys {
		if k.Owner == owner {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) DeleteKey(_ context.Context, owner, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.keys[id]; !ok || k.Owner != owner {
		return ErrNotFound
	}
	delete(m.keys, id)
	return nil
}

func (m *Memory) AddMedium(_ context.Context, x Medium) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.members[x.Owner]; !ok {
		return fmt.Errorf("owner %q isn't a member (foreign key)", x.Owner)
	}
	if _, ok := m.media[x.ID]; ok {
		return ErrExists
	}
	x.CreatedAt = m.now()
	m.media[x.ID] = x
	return nil
}

func (m *Memory) Medium(_ context.Context, id string) (Medium, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	x, ok := m.media[id]
	if !ok {
		return Medium{}, ErrNotFound
	}
	return x, nil
}

func (m *Memory) Media(_ context.Context, owner string) ([]Medium, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Medium
	for _, x := range m.media {
		if x.Owner == owner {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) SetMediumProfile(_ context.Context, id, profile string, virtualBytes int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	x, ok := m.media[id]
	if ok {
		x.Profile = profile
		x.VirtualBytes = max(x.VirtualBytes, virtualBytes)
		m.media[id] = x
	}
	return nil
}

func (m *Memory) DeleteMedium(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.media, id)
	return nil
}
