// Copyright 2026 Abe Diaz (@abe238) and contributors. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"context"
	"strings"
	"testing"
)

const occupancyFixture = "testdata/openshelters-occupancy-SYNTHETIC.json"

// stubOccupancy swaps fetchOccupancy for a deterministic function for the test.
func stubOccupancy(t *testing.T, fn func(context.Context) ([]Shelter, error)) {
	t.Helper()
	prev := fetchOccupancy
	t.Cleanup(func() { fetchOccupancy = prev })
	fetchOccupancy = fn
}

// TestParseOccupancy pins the Open_Shelters contract: UPPERCASE attribute keys,
// records tagged "occupancy", live population + breakdown + capacity, the
// trailing-comma city cleaned, state normalized, the pet code mapped onto the
// FEMA vocabulary, the incident carried, and an unreported population staying nil
// (never coerced to 0).
func TestParseOccupancy(t *testing.T) {
	occ, err := parseOccupancy(readFixture(t, occupancyFixture))
	if err != nil {
		t.Fatalf("parseOccupancy: %v", err)
	}
	if len(occ) != 4 {
		t.Fatalf("occupancy fixture: got %d, want 4", len(occ))
	}
	byName := map[string]Shelter{}
	for _, s := range occ {
		if s.Source != "occupancy" {
			t.Errorf("%s: source = %q, want occupancy", s.Name, s.Source)
		}
		byName[s.Name] = s
	}

	// Riverside: trailing-comma city cleaned, lowercase state upcased, population set.
	riv := byName["Riverside Community Center"]
	if riv.City != "Highland" {
		t.Errorf("Riverside city = %q, want Highland (trailing comma stripped)", riv.City)
	}
	if riv.State != "IN" {
		t.Errorf("Riverside state = %q, want IN", riv.State)
	}
	if riv.TotalPopulation == nil || *riv.TotalPopulation != 30 {
		t.Errorf("Riverside total population = %v, want 30", riv.TotalPopulation)
	}
	if riv.IncidentName != "444-26" {
		t.Errorf("Riverside incident = %q, want 444-26", riv.IncidentName)
	}
	if riv.ShelterOpenDate == "" {
		t.Error("Riverside open date should be rendered from epoch millis")
	}

	// Example Civic Arena: full breakdown + capacity + pet mapping + accessibility.
	arena := byName["Example Civic Arena"]
	if arena.TotalPopulation == nil || *arena.TotalPopulation != 50 {
		t.Errorf("Arena total = %v, want 50", arena.TotalPopulation)
	}
	if arena.MedicalNeedsPopulation == nil || *arena.MedicalNeedsPopulation != 5 {
		t.Errorf("Arena medical = %v, want 5", arena.MedicalNeedsPopulation)
	}
	if arena.PetPopulation == nil || *arena.PetPopulation != 3 {
		t.Errorf("Arena pet pop = %v, want 3", arena.PetPopulation)
	}
	if arena.EvacuationCapacity == nil || *arena.EvacuationCapacity != 200 {
		t.Errorf("Arena evac cap = %v, want 200", arena.EvacuationCapacity)
	}
	if !allowsPets(arena.PetAccommodations) {
		t.Errorf("Arena pets = %q, want a pet-friendly code (Co-Located -> COHABIT)", arena.PetAccommodations)
	}
	if !isYes(arena.ADACompliant) || !isYes(arena.WheelchairAccessible) {
		t.Errorf("Arena accessibility not parsed: ada=%q wheelchair=%q", arena.ADACompliant, arena.WheelchairAccessible)
	}

	// Lakeside: an OPEN shelter with population 0 (empty, not unknown).
	lake := byName["Lakeside Open Standby"]
	if lake.TotalPopulation == nil || *lake.TotalPopulation != 0 {
		t.Errorf("Lakeside total = %v, want 0 (open but empty)", lake.TotalPopulation)
	}

	// Unreported: a null population must stay nil, never coerced to 0.
	un := byName["Unreported Population Site"]
	if un.TotalPopulation != nil {
		t.Errorf("Unreported total = %v, want nil (null, not 0)", un.TotalPopulation)
	}
}

// TestParseOccupancyFailsLoud: a valid-JSON-but-wrong-shape body must error, not
// be read as "0 shelters with population".
func TestParseOccupancyFailsLoud(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"features":null}`, `{"status":"blocked"}`} {
		if _, err := parseOccupancy([]byte(body)); err == nil {
			t.Errorf("parseOccupancy(%q) = nil error, want fail-loud", body)
		}
	}
	// An ArcGIS error object must surface as an error.
	if _, err := parseOccupancy([]byte(`{"error":{"code":400,"message":"bad"}}`)); err == nil {
		t.Error("parseOccupancy(error object) = nil error, want surfaced error")
	}
}

// TestUnionOccupancyFillsAndAppends: a matched base row gets live population and
// "+occupancy" provenance; an unmatched occupancy row is appended as "occupancy".
func TestUnionOccupancyFillsAndAppends(t *testing.T) {
	pop, evac := 42, 300
	base := []Shelter{
		{ShelterID: 1, Name: "Lincoln Community Center", State: "IN", Zip: "46322", Source: "fema"}, // null population
		{ShelterID: 0, Name: "RC Only Site", State: "WA", Zip: "98001", Source: "redcross"},
	}
	occ := []Shelter{
		{Name: "Lincoln Community Center", State: "IN", Zip: "46322", Source: "occupancy", TotalPopulation: &pop, EvacuationCapacity: &evac}, // matches base #1
		{Name: "Open Only Shelter", State: "LA", Zip: "70501", Source: "occupancy", TotalPopulation: &pop},                                   // occupancy-only
	}
	out := unionOccupancy(base, occ)
	if len(out) != 3 {
		t.Fatalf("union size = %d, want 3 (1 filled + 1 untouched + 1 appended)", len(out))
	}
	byName := map[string]Shelter{}
	for _, s := range out {
		byName[s.Name] = s
	}
	lincoln := byName["Lincoln Community Center"]
	if lincoln.TotalPopulation == nil || *lincoln.TotalPopulation != 42 {
		t.Errorf("Lincoln population not filled from occupancy: %v", lincoln.TotalPopulation)
	}
	if lincoln.EvacuationCapacity == nil || *lincoln.EvacuationCapacity != 300 {
		t.Errorf("Lincoln capacity not filled from occupancy: %v", lincoln.EvacuationCapacity)
	}
	if lincoln.Source != "fema+occupancy" {
		t.Errorf("Lincoln source = %q, want fema+occupancy", lincoln.Source)
	}
	if lincoln.ShelterID != 1 {
		t.Errorf("Lincoln lost FEMA shelter_id: %d", lincoln.ShelterID)
	}
	only := byName["Open Only Shelter"]
	if only.Source != "occupancy" || only.TotalPopulation == nil {
		t.Errorf("occupancy-only row mistagged or missing population: %+v", only)
	}
}

// TestUnionOccupancyNoSilentDropOnDoubleMatch: two occupancy rows keying the same
// base row must not both collapse into it; the second is appended, never dropped.
func TestUnionOccupancyNoSilentDropOnDoubleMatch(t *testing.T) {
	p1, p2 := 10, 20
	base := []Shelter{{ShelterID: 1, Name: "Shared Name", State: "TX", Source: "fema"}}
	occ := []Shelter{
		{Name: "Shared Name", State: "TX", Source: "occupancy", TotalPopulation: &p1},
		{Name: "Shared Name", State: "TX", Source: "occupancy", TotalPopulation: &p2},
	}
	out := unionOccupancy(base, occ)
	if len(out) != 2 {
		t.Fatalf("double-match = %d rows, want 2 (one filled, one appended)", len(out))
	}
}

// TestFillOccupancyGapFillOnly: occupancy fills nil population but never clobbers
// a value the spine already reported, and descriptive fields only fill gaps.
func TestFillOccupancyGapFillOnly(t *testing.T) {
	existing, incoming := 5, 99
	dst := Shelter{Address: "Spine Address", TotalPopulation: &existing}
	src := Shelter{Address: "Occupancy Address", TotalPopulation: &incoming, EvacuationCapacity: &incoming}
	fillOccupancy(&dst, src)
	if dst.TotalPopulation == nil || *dst.TotalPopulation != 5 {
		t.Errorf("population clobbered: %v, want the spine's 5", dst.TotalPopulation)
	}
	if dst.Address != "Spine Address" {
		t.Errorf("address clobbered: %q, want the spine's", dst.Address)
	}
	if dst.EvacuationCapacity == nil || *dst.EvacuationCapacity != 99 {
		t.Errorf("capacity not gap-filled: %v, want 99", dst.EvacuationCapacity)
	}

	// When the destination is empty/nil, occupancy fills it.
	empty := Shelter{}
	fillOccupancy(&empty, src)
	if empty.TotalPopulation == nil || *empty.TotalPopulation != 99 {
		t.Errorf("gap not filled: %v, want 99", empty.TotalPopulation)
	}
	if empty.Address != "Occupancy Address" {
		t.Errorf("address gap not filled: %q", empty.Address)
	}
}

// TestAddOccupancySource covers the provenance tag composition.
func TestAddOccupancySource(t *testing.T) {
	cases := map[string]string{
		"":               "occupancy",
		"fema":           "fema+occupancy",
		"redcross":       "redcross+occupancy",
		"fema+redcross":  "fema+redcross+occupancy",
		"fema+occupancy": "fema+occupancy", // idempotent
	}
	for in, want := range cases {
		if got := addOccupancySource(in); got != want {
			t.Errorf("addOccupancySource(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestApplyOccupancyUnion exercises the skip, success, and degrade paths.
func TestApplyOccupancyUnion(t *testing.T) {
	// Skip paths must not call the network.
	stubOccupancy(t, func(context.Context) ([]Shelter, error) {
		t.Fatal("fetchOccupancy must not be called when occupancy is skipped")
		return nil, nil
	})
	for _, tc := range []struct {
		name        string
		flags       *rootFlags
		spineSource string
	}{
		{"no-enrich", &rootFlags{noEnrich: true, dataSource: "auto"}, "live"},
		{"data-source-local", &rootFlags{dataSource: "local"}, "live"},
		{"spine-local", &rootFlags{dataSource: "auto"}, "local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			feed := &shelterFeed{Source: "u", Shelters: []Shelter{{ShelterID: 1, Source: "fema"}}}
			st := applyOccupancyUnion(context.Background(), tc.flags, feed, tc.spineSource)
			if st.OK || st.Note == "" {
				t.Errorf("%s: state = %+v, want skipped with note", tc.name, st)
			}
			if len(feed.Shelters) != 1 {
				t.Errorf("%s: shelters changed despite skip", tc.name)
			}
		})
	}

	// Success path: occupancy fills the spine population.
	pop := 17
	stubOccupancy(t, func(context.Context) ([]Shelter, error) {
		return []Shelter{{Name: "F", State: "IL", Source: "occupancy", TotalPopulation: &pop}}, nil
	})
	feed := &shelterFeed{Source: "https://feed", Shelters: []Shelter{{ShelterID: 1, Name: "F", State: "IL", Source: "fema"}}}
	st := applyOccupancyUnion(context.Background(), &rootFlags{dataSource: "auto"}, feed, "live")
	if !st.OK {
		t.Fatalf("success path: state = %+v, want OK", st)
	}
	if feed.Shelters[0].TotalPopulation == nil || *feed.Shelters[0].TotalPopulation != 17 {
		t.Errorf("occupancy did not fill spine population: %v", feed.Shelters[0].TotalPopulation)
	}
	if !strings.Contains(feed.Shelters[0].Source, "occupancy") {
		t.Errorf("merged row source = %q, want it to record occupancy", feed.Shelters[0].Source)
	}

	// Degrade path: a fetch error must not fail the command.
	stubOccupancy(t, func(context.Context) ([]Shelter, error) {
		return nil, context.DeadlineExceeded
	})
	feed2 := &shelterFeed{Source: "https://feed", Shelters: []Shelter{{ShelterID: 1, Source: "fema"}}}
	st2 := applyOccupancyUnion(context.Background(), &rootFlags{dataSource: "auto"}, feed2, "live")
	if st2.OK || !st2.Attempted || st2.Note == "" {
		t.Errorf("degrade path: state = %+v, want attempted-but-not-OK with a note", st2)
	}
	if len(feed2.Shelters) != 1 {
		t.Errorf("degrade path: shelters changed: %d", len(feed2.Shelters))
	}
}
