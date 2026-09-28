// Package ingest holds source-specific configuration and raw message parsing.
package ingest

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Profile describes one upstream source and how its values are interpreted.
// Higher Priority wins demographic conflict resolution. TimeZone, when set,
// gives wall-clock timestamps without offsets an unambiguous location.
type Profile struct {
	ID          string `json:"id"`
	Priority    int    `json:"priority"`
	TimeZone    string `json:"time_zone"`
	TimeZoneSet bool   `json:"-"`
}

// Catalog is the set of known source profiles.
type Catalog struct {
	profiles map[string]Profile
}

// NewCatalog builds a catalog, rejecting duplicate or invalid profiles.
func NewCatalog(profiles []Profile) (*Catalog, error) {
	c := &Catalog{profiles: map[string]Profile{}}
	for _, p := range profiles {
		if p.ID == "" {
			return nil, fmt.Errorf("ingest: source profile has empty id")
		}
		if _, dup := c.profiles[p.ID]; dup {
			return nil, fmt.Errorf("ingest: duplicate source profile %q", p.ID)
		}
		if p.TimeZone != "" {
			if _, err := time.LoadLocation(p.TimeZone); err != nil {
				return nil, fmt.Errorf("ingest: source %q has invalid time_zone %q: %w",
					p.ID, p.TimeZone, err)
			}
			p.TimeZoneSet = true
		}
		c.profiles[p.ID] = p
	}
	return c, nil
}

// Get returns a known profile.
func (c *Catalog) Get(id string) (Profile, bool) {
	p, ok := c.profiles[id]
	return p, ok
}

// IDs returns known source IDs in sorted order.
func (c *Catalog) IDs() []string {
	out := make([]string, 0, len(c.profiles))
	for id := range c.profiles {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// LoadProfiles decodes a JSON array of source profiles.
func LoadProfiles(raw []byte) ([]Profile, error) {
	var profiles []Profile
	if err := json.Unmarshal(raw, &profiles); err != nil {
		return nil, fmt.Errorf("ingest: invalid sources json: %w", err)
	}
	return profiles, nil
}
