// Copyright 2026 Abe Diaz (@abe238) and contributors. Licensed under Apache-2.0. See LICENSE.
//
// Red Cross union: FEMA's OpenShelters feed is synchronized downstream of the
// American Red Cross database (every morning, then every 20 minutes), so a
// shelter opened today can appear on the public redcross.org map up to a day
// before it shows in FEMA. Neither feed is a superset of the other. To answer
// "every open shelter right now" the CLI unions the FEMA spine with the Red
// Cross Emergency-Action view, deduped on the stable physical identity
// (normalized name + state + ZIP). The fetch is best-effort: if Red Cross is
// unavailable the command degrades to FEMA-only with an honest note, never an
// error.

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// redCrossReferer is required: the Red Cross EA endpoint gates on Referer.
const redCrossReferer = "https://www.redcross.org/"

// redCrossWhere selects the rows to union: publicly shown (hide_from_public='No',
// the redcross.org map's own filter) AND currently open. The status conjunct
// matters because, unlike FEMA's OpenShelters layer (open by source), the Red
// Cross EA view can in principle expose public rows in a non-open status; without
// it the union could label a non-open row "open". Verified live: every
// hide_from_public='No' row is currently 'Open'.
const redCrossWhere = "hide_from_public='No' AND active_site_status='Open'"

// rcOutFields is the exact set of Red Cross columns parseRedCross reads. Like the
// FEMA enrichment, requesting an explicit list (not "*") bounds the response and
// keeps the layer's editor/creator and other metadata columns off the wire.
var rcOutFields = []string{
	"active_site_name", "active_site_address", "active_city", "active_state",
	"active_zip", "active_county", "active_site_status", "active_site_type",
	"active_site_pet_shelter_type", "active_shown_site_id", "dataid", "hide_from_public",
}

// fetchRedCross fetches and parses the Red Cross Emergency-Action shelter view.
// Package var so tests stub it without the network.
var fetchRedCross = redCrossFetch

func redCrossFetch(ctx context.Context) ([]Shelter, error) {
	v := url.Values{}
	v.Set("where", redCrossWhere)
	v.Set("outFields", strings.Join(rcOutFields, ","))
	v.Set("returnGeometry", "true")
	v.Set("outSR", "4326") // return lat/lon directly; avoids Web Mercator math
	v.Set("f", "json")
	body, err := httpGet(ctx, redCrossBase+redCrossQuery+"?"+v.Encode(), redCrossReferer)
	if err != nil {
		return nil, err
	}
	return parseRedCross(body)
}

// rcResponse mirrors the ArcGIS feature shape WITH geometry. The spine and
// enrichment parsers drop geometry, but Red Cross rows carry their coordinates
// only in the geometry, so this parser reads both attributes and geometry.
type rcResponse struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Features []struct {
		Attributes map[string]any `json:"attributes"`
		Geometry   *struct {
			X *float64 `json:"x"`
			Y *float64 `json:"y"`
		} `json:"geometry"`
	} `json:"features"`
}

// parseRedCross decodes the Red Cross EA view into Shelter records (Source
// "redcross"). It fails loud on a valid-JSON-but-wrong-shape body, exactly like
// the spine parser, so a broken or blocked Red Cross feed is never read as
// "0 Red Cross shelters".
func parseRedCross(raw []byte) ([]Shelter, error) {
	var resp rcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parsing Red Cross feed: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("Red Cross service error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Features == nil {
		// nil for {} / null / {"features":null}; non-nil (even []) for a real result.
		return nil, fmt.Errorf("unrecognized Red Cross response: valid JSON but no 'features' array and no 'error' (the feed shape may have changed)")
	}
	out := make([]Shelter, 0, len(resp.Features))
	for _, f := range resp.Features {
		a := f.Attributes
		if a == nil {
			continue
		}
		s := Shelter{Source: "redcross"}
		s.Name = attrStr(a, "active_site_name")
		s.Address = attrStr(a, "active_site_address")
		s.City = attrStr(a, "active_city")
		s.State = normCode(attrStr(a, "active_state"))
		s.Zip = strings.TrimSpace(attrStr(a, "active_zip"))
		s.CountyParish = attrStr(a, "active_county")
		s.Status = normCode(attrStr(a, "active_site_status"))
		s.FacilityType = normCode(attrStr(a, "active_site_type"))
		s.PetAccommodations = rcPetCode(attrStr(a, "active_site_pet_shelter_type"))
		s.RedCrossID = rcID(a)
		// Some RC rows leave city/state/zip blank but carry them in the address
		// tail ("..., Chicago, IL 60612"); fill the blanks best-effort.
		if s.City == "" || s.State == "" || s.Zip == "" {
			c, st, z := parseAddressTail(s.Address)
			if s.City == "" {
				s.City = c
			}
			if s.State == "" {
				s.State = normCode(st)
			}
			if s.Zip == "" {
				s.Zip = z
			}
		}
		// outSR=4326 geometry: x is longitude, y is latitude.
		if f.Geometry != nil && f.Geometry.X != nil && f.Geometry.Y != nil {
			lat, lon := *f.Geometry.Y, *f.Geometry.X
			s.Latitude, s.Longitude = &lat, &lon
		}
		out = append(out, s)
	}
	return out, nil
}

// rcID returns the Red Cross site id as a string (active_shown_site_id preferred,
// else dataid), or "" when neither is present.
func rcID(a map[string]any) string {
	for _, k := range []string{"active_shown_site_id", "dataid"} {
		if v, ok := a[k].(float64); ok {
			return strconv.FormatInt(int64(v), 10)
		}
		if v, ok := a[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// rcPetCode maps a Red Cross pet-shelter-type onto the FEMA coded vocabulary,
// conservatively. Only clearly co-located / on-site arrangements count as
// pet-friendly (allowsPets); "off-site" (pets sheltered elsewhere) is preserved
// as OFFSITE but is NOT treated as on-site pet-friendly; unknown stays "".
func rcPetCode(v string) string {
	s := normCode(v)
	switch {
	case s == "":
		return ""
	case strings.Contains(s, "CO-LOCAT") || strings.Contains(s, "COLOCAT") || strings.Contains(s, "COHABIT") || strings.Contains(s, "WITH OWNER"):
		return "COHABIT"
	case strings.Contains(s, "ON-SITE") || strings.Contains(s, "ONSITE"):
		return "ONSITE"
	case strings.Contains(s, "OFF-SITE") || strings.Contains(s, "OFFSITE"):
		return "OFFSITE"
	case strings.Contains(s, "NONE") || strings.Contains(s, "NO PET") || s == "NO":
		return "NONE"
	default:
		return ""
	}
}

var rcAddrTailRe = regexp.MustCompile(`,\s*([^,]+?),\s*([A-Za-z]{2})\s+(\d{5})(?:-\d{4})?\s*$`)

// parseAddressTail best-effort extracts (city, state, zip) from the tail of a
// US one-line address like "123 Main St, Springfield, IL 62704". Returns blanks
// when the tail does not match (e.g. a redacted "[address omitted ...]").
func parseAddressTail(addr string) (city, state, zip string) {
	m := rcAddrTailRe.FindStringSubmatch(addr)
	if m == nil {
		return "", "", ""
	}
	return strings.TrimSpace(m[1]), strings.ToUpper(m[2]), m[3]
}

// --- union / dedup ---

// nonAlnumRe strips everything but lowercase letters and digits so names that
// differ only by punctuation/spacing/case collapse to the same key
// ("RAUNER CENTER-TRAINING ROOM" and "Rauner Center - Training Room"). It is
// exact-after-normalization: it does NOT fold abbreviation variants (St./Saint,
// HS/High School, &/and), so two records for the same shelter that differ by such
// a variant will not dedup and will both appear (tagged by their source). That is
// the safe failure mode: over-listing rather than wrongly merging two distinct
// shelters into one.
var nonAlnumRe = regexp.MustCompile(`[^a-z0-9]+`)

func normName(s string) string {
	return nonAlnumRe.ReplaceAllString(strings.ToLower(s), "")
}

// zip5 returns the 5-digit ZIP prefix, or "" if the value is not a clean ZIP.
func zip5(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 5 {
		s = s[:5]
	}
	if len(s) != 5 {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

// unionFeeds returns the FEMA spine unioned with Red Cross records, deduped. A
// Red Cross record is the same physical shelter as a FEMA record when their
// alphanumeric-normalized names and states match AND their 5-digit ZIPs are
// compatible (equal, or at least one missing). On a match the FEMA record wins
// (it carries shelter_id + enrichment), any still-empty FEMA fields are filled
// from Red Cross, and it is tagged "fema+redcross"; unmatched Red Cross records
// are appended as "redcross". The FEMA slice is treated as authoritative; a copy
// is returned so the caller's slice is not mutated underneath it.
func unionFeeds(fema, rc []Shelter) []Shelter {
	out := make([]Shelter, len(fema))
	copy(out, fema)
	idx := map[string][]int{}
	for i := range out {
		k := normName(out[i].Name) + "|" + out[i].State
		idx[k] = append(idx[k], i)
	}
	merged := map[int]bool{} // FEMA indices already absorbed by an RC row
	for _, r := range rc {
		if normName(r.Name) == "" {
			continue // no usable name -> cannot dedup or present; never key as "|STATE"
		}
		k := normName(r.Name) + "|" + r.State
		matched := -1
		for _, ci := range idx[k] {
			// Only match an un-consumed FEMA row; a second distinct RC row that
			// keys the same must NOT be absorbed into the same FEMA row (which
			// would silently drop it) — it falls through and is appended instead.
			if !merged[ci] && out[ci].Source != "redcross" && zipCompatible(out[ci], r) {
				matched = ci
				break
			}
		}
		if matched >= 0 {
			fillEmptyFrom(&out[matched], r)
			out[matched].Source = "fema+redcross"
			if out[matched].RedCrossID == "" {
				out[matched].RedCrossID = r.RedCrossID
			}
			merged[matched] = true
		} else {
			out = append(out, r)
		}
	}
	return out
}

// zipCompatible reports whether two records' ZIPs do not contradict: equal when
// both are clean ZIPs, otherwise compatible (at least one missing).
func zipCompatible(a, b Shelter) bool {
	za, zb := zip5(a.Zip), zip5(b.Zip)
	if za != "" && zb != "" {
		return za == zb
	}
	return true
}

// fillEmptyFrom copies non-empty Red Cross fields into the FEMA record ONLY where
// the FEMA field is empty/nil, so the FEMA spine and its enrichment always win
// and Red Cross merely fills gaps (notably coordinates, which the FEMA feed
// frequently omits). Only same-concept fields are crossed over: Status and
// FacilityType are deliberately NOT filled from Red Cross, because they use a
// different coded vocabulary than FEMA's (active_site_status / active_site_type
// vs the OpenShelters and FEMA_NSS codes) and downstream filters read them as
// FEMA codes; surfacing a Red Cross value under a FEMA-named coded field would
// misattribute. PetAccommodations is safe to cross because rcPetCode maps it onto
// the FEMA vocabulary first.
func fillEmptyFrom(dst *Shelter, src Shelter) {
	if dst.Address == "" {
		dst.Address = src.Address
	}
	if dst.City == "" {
		dst.City = src.City
	}
	if dst.Zip == "" {
		dst.Zip = src.Zip
	}
	if dst.CountyParish == "" {
		dst.CountyParish = src.CountyParish
	}
	if dst.PetAccommodations == "" {
		dst.PetAccommodations = src.PetAccommodations
	}
	if dst.Latitude == nil && dst.Longitude == nil && src.Latitude != nil && src.Longitude != nil {
		dst.Latitude, dst.Longitude = src.Latitude, src.Longitude
	}
}

// applyRedCrossUnion best-effort unions the Red Cross feed into the spine in
// place and reports what happened. Skipped (no network) under --no-enrich or
// offline data sources; a fetch failure degrades to FEMA-only with a note, never
// an error.
func applyRedCrossUnion(ctx context.Context, flags *rootFlags, feed *shelterFeed, spineSource string) enrichState {
	if flags.noEnrich {
		return enrichState{Note: "Red Cross union skipped (--no-enrich); FEMA OpenShelters spine only."}
	}
	if flags.dataSource == "local" || spineSource == "local" {
		return enrichState{Note: "Red Cross union skipped (offline / --data-source local); FEMA OpenShelters spine only."}
	}
	rc, err := fetchRedCross(ctx)
	if err != nil {
		return enrichState{Attempted: true, Note: "Red Cross feed unavailable; results are FEMA OpenShelters only. Spine data is unaffected."}
	}
	before := len(feed.Shelters)
	feed.Shelters = unionFeeds(feed.Shelters, rc)
	added := len(feed.Shelters) - before
	feed.Source = "FEMA OpenShelters + Red Cross Shelters_Prod_EA_view (union)"
	return enrichState{
		Attempted: true,
		OK:        true,
		Note:      fmt.Sprintf("Unioned %d Red Cross record(s); %d open shelter(s) not in the FEMA feed.", len(rc), added),
	}
}
