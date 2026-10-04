package sms

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// HashKey is how API keys are stored: the platform never keeps a key, only its
// SHA-256, and compares in constant time through the map lookup of the digest.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Snapshot is a consistent copy of the directory, from which the router is
// rebuilt.
type Snapshot struct {
	Users       map[string]User
	Providers   []ProviderView
	Assignments []Assignment
}

type providerEntry struct {
	view    ProviderView
	dynamic bool
}

// Directory is the in-memory view of users, providers, assignments and rates.
// The store is the source of truth; the directory is what the hot path reads,
// and it is kept current by configuration events.
type Directory struct {
	mu          sync.RWMutex
	users       map[string]User
	byKey       map[string]string
	providers   map[string]*providerEntry
	assignments map[string]Assignment
	rates       map[string]Rate
	baseRates   []Rate
}

// NewDirectory returns an empty directory.
func NewDirectory() *Directory {
	return &Directory{
		users: map[string]User{}, byKey: map[string]string{},
		providers: map[string]*providerEntry{}, assignments: map[string]Assignment{}, rates: map[string]Rate{},
	}
}

// SetBaseRates installs the platform's configured price list. Rates stored at
// runtime override it at equal specificity.
func (d *Directory) SetBaseRates(r []Rate) {
	d.mu.Lock()
	d.baseRates = append([]Rate(nil), r...)
	d.mu.Unlock()
}

// PutUser adds or replaces a user.
func (d *Directory) PutUser(u User) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if old, ok := d.users[u.ID]; ok && old.APIKeyHash != "" {
		delete(d.byKey, old.APIKeyHash)
	}
	d.users[u.ID] = u
	if u.APIKeyHash != "" {
		d.byKey[u.APIKeyHash] = u.ID
	}
}

// RemoveUser deletes a user.
func (d *Directory) RemoveUser(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if old, ok := d.users[id]; ok {
		delete(d.byKey, old.APIKeyHash)
	}
	delete(d.users, id)
}

// User returns a user by id.
func (d *Directory) User(id string) (User, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	u, ok := d.users[id]
	return u, ok
}

// UserByKey resolves an API key to its user.
func (d *Directory) UserByKey(key string) (User, bool) {
	hash := HashKey(key)
	d.mu.RLock()
	defer d.mu.RUnlock()
	id, ok := d.byKey[hash]
	if !ok {
		return User{}, false
	}
	u, ok := d.users[id]
	return u, ok
}

// Users lists users by id.
func (d *Directory) Users() []User {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]User, 0, len(d.users))
	for _, u := range d.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PutProvider adds or replaces a provider's configuration.
func (d *Directory) PutProvider(name string, cfg ProviderConfig, dynamic bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.providers[name] = &providerEntry{view: ProviderView{Name: name, Config: cfg, Owner: cfg.Owner, Disabled: cfg.Disabled}, dynamic: dynamic}
}

// RemoveProvider deletes a provider.
func (d *Directory) RemoveProvider(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.providers, name)
}

// Provider returns a provider's configuration.
func (d *Directory) Provider(name string) (ProviderView, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	e, ok := d.providers[name]
	if !ok {
		return ProviderView{}, false
	}
	return e.view, true
}

// Providers lists providers by name.
func (d *Directory) Providers() []ProviderView {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]ProviderView, 0, len(d.providers))
	for _, e := range d.providers {
		out = append(out, e.view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// PutAssignment adds or replaces an assignment.
func (d *Directory) PutAssignment(a Assignment) {
	d.mu.Lock()
	d.assignments[a.ID] = a
	d.mu.Unlock()
}

// RemoveAssignment deletes an assignment.
func (d *Directory) RemoveAssignment(id string) {
	d.mu.Lock()
	delete(d.assignments, id)
	d.mu.Unlock()
}

// Assignments lists assignments by id.
func (d *Directory) Assignments() []Assignment {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Assignment, 0, len(d.assignments))
	for _, a := range d.assignments {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PutRate adds or replaces a rate.
func (d *Directory) PutRate(r Rate) {
	d.mu.Lock()
	d.rates[r.ID] = r
	d.mu.Unlock()
}

// RemoveRate deletes a rate.
func (d *Directory) RemoveRate(id string) {
	d.mu.Lock()
	delete(d.rates, id)
	d.mu.Unlock()
}

// Rates lists the runtime rates by id.
func (d *Directory) Rates() []Rate {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Rate, 0, len(d.rates))
	for _, r := range d.rates {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Snapshot copies the directory for a router rebuild.
func (d *Directory) Snapshot() Snapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	s := Snapshot{Users: make(map[string]User, len(d.users))}
	for id, u := range d.users {
		s.Users[id] = u
	}
	for _, e := range d.providers {
		s.Providers = append(s.Providers, e.view)
	}
	sort.Slice(s.Providers, func(i, j int) bool { return s.Providers[i].Name < s.Providers[j].Name })
	for _, a := range d.assignments {
		s.Assignments = append(s.Assignments, a)
	}
	sort.Slice(s.Assignments, func(i, j int) bool { return s.Assignments[i].ID < s.Assignments[j].ID })
	return s
}

// ErrNoRate means no price covers the destination.
type ErrNoRate struct{ Country, Type string }

func (e *ErrNoRate) Error() string {
	return fmt.Sprintf("no price is configured for %s messages to %s", e.Type, e.Country)
}

// Price returns what a user pays per segment for a destination. The most
// specific matching rate wins: a user's own rate first, then the rate matching
// the most of country and message type (country outranks type on a tie), then
// the default. A runtime rate wins a tie with the platform's configured one.
func (d *Directory) Price(user, country, msgType string) (int64, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	best, bestScore, found := int64(0), -1, false
	consider := func(r Rate, dynamic bool) {
		if r.UserID != "" && r.UserID != user {
			return
		}
		if r.Country != "" && !strings.EqualFold(r.Country, country) {
			return
		}
		if r.MessageType != "" && !strings.EqualFold(r.MessageType, msgType) {
			return
		}
		// A user's own rate outranks every general one. Among general rates, one
		// that matches more dimensions (country and type) beats one that matches
		// fewer, and on a tie the country outranks the type.
		score, dims := 0, 0
		if r.UserID != "" {
			score += 1000
		}
		if r.Country != "" {
			dims++
			score += 2
		}
		if r.MessageType != "" {
			dims++
			score++
		}
		score += dims * 10
		score *= 2
		if dynamic {
			score++
		}
		if score > bestScore {
			best, bestScore, found = r.SellPerSegmentMicros, score, true
		}
	}
	for _, r := range d.baseRates {
		consider(r, false)
	}
	for _, r := range d.rates {
		consider(r, true)
	}
	if !found {
		return 0, &ErrNoRate{Country: country, Type: msgType}
	}
	return best, nil
}
