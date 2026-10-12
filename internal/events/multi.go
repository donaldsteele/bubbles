package events

// MultiPublisher hands every event to several Publishers, in the order they
// were given. Install it with SetPublisher to let the notification dispatcher
// see the events the hub sees. Each Publisher must not block (see Publisher),
// so the writer goroutine is never held up by a slow consumer.
type MultiPublisher struct {
	ps []Publisher
}

// NewMultiPublisher returns a Publisher that forwards to ps. nil entries are
// skipped. The set is fixed at construction.
func NewMultiPublisher(ps ...Publisher) *MultiPublisher {
	m := &MultiPublisher{}
	for _, p := range ps {
		if p != nil {
			m.ps = append(m.ps, p)
		}
	}
	return m
}

// Publish forwards e to every Publisher.
func (m *MultiPublisher) Publish(e Event) {
	for _, p := range m.ps {
		p.Publish(e)
	}
}
