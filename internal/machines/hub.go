package machines

import "context"

// Event is a change to one of a member's machines: kind "vm" (data: Machine) or "deleted" (data:
// {"id": ...}).
type Event struct {
	Kind string
	Data any
}

// change says a machine of owner changed. It carries no state: the hub reads the machine from the
// cache when it sends, so an event can never carry a state older than one sent before it.
type change struct {
	owner  string
	id     string
	medium bool // a medium, not a machine
}

type subscriber struct {
	owner string
	ch    chan Event
}

// hub fans the informers' changes out to the members' event streams. One goroutine owns the
// subscriber set and builds every event; everything else talks to it through channels.
type hub struct {
	sub    chan *subscriber
	unsub  chan *subscriber
	pub    chan change
	done   <-chan struct{}
	render func(change) Event
}

// A stream that falls this far behind is closed; its client reconnects and gets a new snapshot.
const subscriberBuffer = 64

// newHub runs the hub until ctx ends. render builds the event for a change from current state.
func newHub(ctx context.Context, render func(change) Event) *hub {
	h := &hub{sub: make(chan *subscriber), unsub: make(chan *subscriber), pub: make(chan change, 256), done: ctx.Done(), render: render}
	go h.run(ctx)
	return h
}

func (h *hub) run(ctx context.Context) {
	subs := map[*subscriber]bool{}
	drop := func(s *subscriber) {
		if subs[s] {
			delete(subs, s)
			close(s.ch)
		}
	}
	for {
		select {
		case <-ctx.Done():
			for s := range subs {
				drop(s)
			}
			return
		case s := <-h.sub:
			subs[s] = true
		case s := <-h.unsub:
			drop(s)
		case c := <-h.pub:
			var e *Event
			for s := range subs {
				if s.owner != c.owner {
					continue
				}
				if e == nil {
					rendered := h.render(c)
					e = &rendered
				}
				select {
				case s.ch <- *e:
				default:
					drop(s)
				}
			}
		}
	}
}

// The sends give up once the hub has stopped (shutdown), so no caller blocks on it.

func (h *hub) publish(c change) {
	select {
	case h.pub <- c:
	case <-h.done:
	}
}

func (h *hub) subscribe(owner string) (<-chan Event, func()) {
	s := &subscriber{owner: owner, ch: make(chan Event, subscriberBuffer)}
	select {
	case h.sub <- s:
	case <-h.done:
		close(s.ch)
		return s.ch, func() {}
	}
	return s.ch, func() {
		select {
		case h.unsub <- s:
		case <-h.done:
		}
	}
}
