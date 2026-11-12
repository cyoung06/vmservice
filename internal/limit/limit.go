// Package limit caps each member's open long-lived connections: event streams, consoles and SSH
// sessions through the bastion, together.
package limit

import (
	"sync"
	"sync/atomic"
)

// PerOwner counts each owner's open connections against Max.
type PerOwner struct {
	Max  int64
	open sync.Map // owner -> *atomic.Int64
}

// Take reserves one of owner's connections; release gives it back. false: the owner is at Max.
func (p *PerOwner) Take(owner string) (release func(), ok bool) {
	v, _ := p.open.LoadOrStore(owner, new(atomic.Int64))
	n := v.(*atomic.Int64)
	if n.Add(1) > p.Max {
		n.Add(-1)
		return nil, false
	}
	return func() { n.Add(-1) }, true
}
