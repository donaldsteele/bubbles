package events

import "container/list"

// lru is a string-keyed map bounded to max entries; inserting beyond the bound
// evicts the least recently used entry. It is not safe for concurrent use:
// the owner's lock guards it, and get counts as a use.
type lru[V any] struct {
	max   int
	order *list.List // front = most recently used
	items map[string]*list.Element
}

type lruEntry[V any] struct {
	key string
	val V
}

func newLRU[V any](max int) *lru[V] {
	return &lru[V]{max: max, order: list.New(), items: map[string]*list.Element{}}
}

func (l *lru[V]) get(key string) (V, bool) {
	el, ok := l.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	l.order.MoveToFront(el)
	return el.Value.(*lruEntry[V]).val, true
}

func (l *lru[V]) put(key string, val V) {
	if el, ok := l.items[key]; ok {
		el.Value.(*lruEntry[V]).val = val
		l.order.MoveToFront(el)
		return
	}
	l.items[key] = l.order.PushFront(&lruEntry[V]{key: key, val: val})
	for l.order.Len() > l.max {
		back := l.order.Back()
		l.order.Remove(back)
		delete(l.items, back.Value.(*lruEntry[V]).key)
	}
}

func (l *lru[V]) remove(key string) {
	if el, ok := l.items[key]; ok {
		l.order.Remove(el)
		delete(l.items, key)
	}
}

func (l *lru[V]) reset() {
	l.order.Init()
	l.items = map[string]*list.Element{}
}

func (l *lru[V]) len() int { return l.order.Len() }
