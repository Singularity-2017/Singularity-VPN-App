package protocols

import "errors"

// Err_unknown_protocol is what Get returns when no adapter was registered
// under the requested name.
var Err_unknown_protocol = errors.New("protocols: no adapter for this protocol")

// Err_duplicate_protocol is what New_registry returns when two adapters
// claim the same name. That is a programming mistake, and the silent
// version of it -- last one wins -- is a tunnel nobody can account for.
var Err_duplicate_protocol = errors.New("protocols: an adapter with this name is already registered")

// Registry is a fixed, ordered set of protocol adapters, built once at
// startup.
type Registry struct {
	order   []Protocol
	by_name map[string]Protocol
}

// New_registry builds a registry from the given adapters. Two adapters
// registered under one name is refused rather than resolved last-wins,
// because that silent choice is exactly the kind of tunnel nobody could
// account for later.
func New_registry(adapters ...Protocol) (*Registry, error) {
	registry := &Registry{
		by_name: make(map[string]Protocol, len(adapters)),
	}
	for _, adapter := range adapters {
		name := adapter.Name()
		if _, exists := registry.by_name[name]; exists {
			return nil, Err_duplicate_protocol
		}
		registry.by_name[name] = adapter
		registry.order = append(registry.order, adapter)
	}
	return registry, nil
}

// Get returns the adapter registered under name.
func (r *Registry) Get(name string) (Protocol, error) {
	adapter, exists := r.by_name[name]
	if !exists {
		return nil, Err_unknown_protocol
	}
	return adapter, nil
}

// Available returns every registered adapter in registration order. The
// order is deterministic on purpose: part B walks this list to choose a
// fallback, and a map iteration would make that choice differ between runs.
func (r *Registry) Available() []Protocol {
	available := make([]Protocol, len(r.order))
	copy(available, r.order)
	return available
}
