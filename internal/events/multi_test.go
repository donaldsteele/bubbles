package events

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

type recordPublisher struct {
	mu   sync.Mutex
	seqs []int64
}

func (r *recordPublisher) Publish(e Event) {
	r.mu.Lock()
	r.seqs = append(r.seqs, e.Seq)
	r.mu.Unlock()
}

func (r *recordPublisher) got() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.seqs...)
}

func TestMultiPublisherFansOutInOrder(t *testing.T) {
	a, b := &recordPublisher{}, &recordPublisher{}
	m := NewMultiPublisher(a, nil, b)
	for seq := int64(1); seq <= 5; seq++ {
		m.Publish(Event{Seq: seq, Type: TypeMessageCreated})
	}
	for name, r := range map[string]*recordPublisher{"a": a, "b": b} {
		got := r.got()
		if len(got) != 5 {
			t.Fatalf("%s got %v, want 5 events", name, got)
		}
		for i, seq := range got {
			if seq != int64(i+1) {
				t.Errorf("%s: event %d has seq %d, want %d", name, i, seq, i+1)
			}
		}
	}
}

func TestMultiPublisherReceivesAppendedEvents(t *testing.T) {
	db := openDB(t)
	rec := &recordPublisher{}
	hub := NewHub(HubOptions{DB: db})
	defer hub.Close()
	SetPublisher(db, NewMultiPublisher(hub, rec))
	for range 3 {
		err := db.Do(context.Background(), func(tx *store.Tx) error {
			_, err := Append(tx, Event{Type: TypeSettingsUpdated})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	got := rec.got()
	if len(got) != 3 || got[0] >= got[1] || got[1] >= got[2] {
		t.Fatalf("seqs = %v, want three ascending", got)
	}
}

func TestHubOnline(t *testing.T) {
	w := newWorld(t)
	if w.hub.Online("alice") {
		t.Fatal("alice is online without a connection")
	}
	alice := w.subscribe("alice", protocol.RoleMember, "")
	if !w.hub.Online("alice") {
		t.Error("alice is not online right after connecting")
	}
	w.clock.Advance(ActivityWindow)
	if w.hub.Online("alice") {
		t.Error("alice is online after the activity window (she is idle)")
	}
	alice.SetPresence(protocol.PresenceActive)
	if !w.hub.Online("alice") {
		t.Error("alice is not online after an active report")
	}
	alice.Leave()
	w.clock.Advance(OfflineGrace + time.Second)
	if w.hub.Online("alice") {
		t.Error("alice is online after her last connection ended")
	}
}

// orderPublisher appends "<name>:<seq>" to a shared log before forwarding.
type orderPublisher struct {
	name string
	mu   *sync.Mutex
	log  *[]string
	next Publisher
}

func (o orderPublisher) Publish(e Event) {
	o.mu.Lock()
	*o.log = append(*o.log, o.name+":"+itoa(e.Seq))
	o.mu.Unlock()
	if o.next != nil {
		o.next.Publish(e)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// The app installs NewMultiPublisher(hub, notify): for every committed event
// the hub must see it before the notification dispatcher does, across events.
func TestMultiPublisherHubSeesEachEventBeforeNotify(t *testing.T) {
	db := openDB(t)
	hub := NewHub(HubOptions{DB: db})
	defer hub.Close()
	var (
		mu  sync.Mutex
		log []string
	)
	SetPublisher(db, NewMultiPublisher(
		orderPublisher{name: "hub", mu: &mu, log: &log, next: hub},
		orderPublisher{name: "notify", mu: &mu, log: &log},
	))
	const n = 4
	for range n {
		err := db.Do(context.Background(), func(tx *store.Tx) error {
			_, err := Append(tx, Event{Type: TypeSettingsUpdated})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(log) != 2*n {
		t.Fatalf("log = %v, want %d entries", log, 2*n)
	}
	seenHub := map[string]bool{}
	for _, entry := range log {
		name, seq, _ := strings.Cut(entry, ":")
		switch name {
		case "hub":
			seenHub[seq] = true
		case "notify":
			if !seenHub[seq] {
				t.Errorf("notify saw seq %s before the hub: %v", seq, log)
			}
		}
	}
}
