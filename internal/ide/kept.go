package ide

import "slices"

// maxKept is how many requests waiting for the IDE, and how many diff tabs
// the VM opened, a link's filter keeps. Claude Code waits for a few
// answers at a time and opens a tab for each change it shows. An entry
// holds at most maxNameBytes of name and ID, so a VM that sends more
// can't make the filter grow beyond a few MiB.
const maxKept = 1000

// kept is a map that holds at most maxKept entries. A new entry beyond
// them drops the oldest, so that what the VM sends can't make it grow
// without end.
type kept[V any] struct {
	values map[string]V
	// order has the keys of values, the oldest first
	order []string
}

func newKept[V any]() *kept[V] {
	return &kept[V]{values: map[string]V{}}
}

func (k *kept[V]) put(key string, value V) {
	if _, has := k.values[key]; !has {
		k.order = append(k.order, key)
	}

	k.values[key] = value

	if len(k.order) > maxKept {
		delete(k.values, k.order[0])
		k.order = slices.Delete(k.order, 0, 1)
	}
}

// take returns the entry and removes it.
func (k *kept[V]) take(key string) (V, bool) {
	value, has := k.values[key]
	if has {
		delete(k.values, key)

		i := slices.Index(k.order, key)
		k.order = slices.Delete(k.order, i, i+1)
	}

	return value, has
}

func (k *kept[V]) clear() {
	clear(k.values)
	k.order = nil
}
