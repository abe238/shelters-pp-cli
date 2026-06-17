# shelters-pp-cli — Locked Build Decisions

Agent-native CLI over FEMA's National Shelter System (NSS) **OpenShelters** feed —
the authoritative source for which shelters are open, where, and (when reported)
how many people are inside during disasters. Same playbook as `nhc-pp-cli`:
verify live for real, capture real active-disaster fixtures as acceptance tests,
generate with the Printing Press, harden via adversarial review, publish.

## Identity
- **CLI:** `shelters-pp-cli` · **MCP:** `shelters-pp-mcp` · Claude Code + OpenClaw skills
- **Spec name:** `shelters` (press appends `-pp-cli`)
- **Repo:** `github.com/abe238/shelters-pp-cli` (public) · local build dir `/Users/abediaz/ghostex/chats/shelters-pp`
- **Mission:** help agents + people answer "where can I shelter right now?" — incl.
  "closest open shelter that takes pets" and "which shelters are at capacity" — from
  the authoritative FEMA feed, honestly (never fabricating missing data).

## Verified source (live HTTP, 2026-06-16)
- **Endpoint (stable):** `https://gis.fema.gov/arcgis/rest/services/NSS/OpenShelters/FeatureServer/0/query`
- **ArcGIS Feature Service.** Params: `where=1=1&outFields=*&returnGeometry=false&f=json&resultRecordCount=2000`. **No API key.** HTTP 200, valid JSON.
- **Layer:** "FEMA Open Shelters", 22 fields. Live now: **6 shelters open** (quiet baseline; spikes only during named events).
- **Do NOT** follow links to `MapServer/...` exports; `FeatureServer/0/query` is the stable URL.

### Real 22-field schema (verified)
`objectid` (OID, **unstable — never join on it**), `shelter_id` (Integer, **STABLE join key**),
`shelter_name`, `address`, `city`, `state`, `zip`, `shelter_status`,
`evacuation_capacity` (Int), `post_impact_capacity` (Int), `total_population` (Int),
`hours_open` (**String**), `hours_close` (**String**), `org_name`, `org_id`, `match_type`,
`subfacility_code`, `ada_compliant`, `pet_accommodations_code`, `wheelchair_accessible`,
`latitude` (Double), `longitude` (Double).

### Authoritative coded vocabulary (sourced from FEMA's OWN filtered layers — not guessed)
The OpenShelters service exposes NO coded-value domains. Recovered the real vocabularies from the
sibling `NSS/FEMA_NSS/FeatureServer` service, whose layers are FEMA-defined filtered views over the
same shelter records (distinct-value queries across 71,498 facility rows, verified live 2026-06-16):
- **pet_accommodations_code:** `NONE` (and blank / `None`) = no pet accommodations; **`COHABIT`** =
  pets cohabitate with their owners in the shelter; **`ONSITE`** = pets sheltered onsite (separate area).
  FEMA's own **"Pet Accommodations" layer (13)** contains exactly `{COHABIT, ONSITE}` → that IS the
  authoritative "allows pets" definition. `--pets` filters to `pet_accommodations_code IN (COHABIT, ONSITE)`.
- **ada_compliant / wheelchair_accessible:** `YES` / `NO` / `UNK` (and blank / `None`). FEMA's
  "ADA Compliant" (8) and "Wheelchair Accessible" (7) layers contain only `YES` → `--ada` / `--wheelchair`
  filter to `= YES`. Treat `UNK`/`NO`/blank as "not confirmed accessible" (do not claim accessibility).
- **shelter_status:** the live OpenShelters feed shows `OPEN` (6, verified). FEMA's FEMA_NSS layer set is
  named Open/Closed/**Full**/Alert, which implies a `{OPEN, CLOSED, FULL, ALERT}` operational taxonomy —
  but this is **NOT verified from a data row** (layer 2 "Full" = 0 rows; PDC RAPIDS metadata does NOT
  document the `shelter_status` domain). So: **computed utilization is the PRIMARY at-capacity signal.**
  `capacity` ALSO surfaces a `FULL` status *defensively if the feed reports it* (case-insensitive), but
  docs must NOT call `FULL` authoritative or guaranteed — it is "if present." Never invent a denominator.
- **match_type:** `A` / `M` (and blank) — internal record-match provenance; surfaced verbatim, not filtered on.
- **Normalization:** distinct values include `'NONE'` vs `'None'` vs `' '` and `UNK` vs blank → **trim +
  uppercase every coded field before any comparison/filter** (else filters miss rows).
- **Addresses present:** all 6 live rows carry real street addresses (e.g. "2450 Lincoln Street",
  "2200 W. HARRISON ST") → `near` geocodes the FULL street address (accurate, not ZIP-centroid).

### Field-value + null contracts (verified quiet-state; active-state values from Wayback fixture)
- **Quiet state (today, 6 shelters):** `shelter_status=OPEN`; `pet_accommodations_code=NONE`;
  `ada_compliant=UNK`; `wheelchair_accessible=UNK`; `latitude/longitude/total_population/
  evacuation_capacity/post_impact_capacity/org_name/hours_open` = **null** for all 6.
- **lat/lon are frequently null** even when open → geocode from address+city+state+zip.
  Verified the **US Census geocoder** (free, no key) returns coords for a shelter address.
- **Capacities + population are mostly null** → only compute utilization where BOTH
  `total_population` and a capacity exist; otherwise report "unknown." Never invent a denominator.
- **shelter_id stable across snapshots; objectid is not.** Join on `shelter_id`.
- **Empty/few result is normal** (0–few hundred when no major disaster; big spikes during named events).
- **Time fields are strings**, not datetimes — parse explicitly.
- **Not real-time:** NSS reports ~2x/day (MIDDAY + OVERNIGHT); state flips only when an
  Emergency Manager updates the record. Poll ~30 min in crisis; more is wasted. The
  snapshot has **no server timestamp** — record the fetch time client-side (UTC).
- **Full NSS** (beyond public OpenShelters) requires an MOU/auth path — out of scope; link-out only.

## Command surface (agent-native)
- `shelters` / `list` — open shelters; filters `--state ST`, `--pets`, `--ada`, `--wheelchair`, `--org`, `--status`.
- `shelter <shelter_id>` — full detail for one shelter (join on shelter_id).
- `near <location> [--pets] [--ada] [--wheelchair] [--limit N] [--max-miles M]` — **closest open
  shelters** to a zip / `lat,lon` / address. Geocodes the location AND any shelter with null
  lat/lon (Census, cached), computes haversine distance, sorts ascending, applies filters.
  **Use case: "closest shelter to me that allows pets."**
- `capacity [--state ST]` — at-capacity view. **Primary denominator = `evacuation_capacity`** (pre-hazard,
  20 sqft/person); each row labels which denominator its % uses. If `evacuation_capacity` is null but
  `post_impact_capacity` (40 sqft/person, reduced) exists, compute against that and label it explicitly.
  Utilization % only where `total_population` + a capacity both exist; mark "unknown" otherwise (never
  invent a denominator). ALSO surfaces any row whose `shelter_status` is `FULL` (case-insensitive) as
  reported-full *if present* — not asserted authoritative. Reports computable count vs unknown count.
  **Use case: "which shelter is at capacity?"**
- `brief [--state] [--markdown]` — situational: open count, by state, pet-friendly count,
  capacity-known + any at-capacity; `--markdown` adds the gratitude footer.
- `gis-links` — link-out to the FeatureServer + the MOU/full-NSS path (not ingested).
- `credits` — the gratitude note + disclaimer.
- Output: JSON-first (`--json`/`--agent`), human/markdown, token-efficient. Every snapshot records `fetched_at` (client UTC).

## Tone + gratitude note (user-requested)
Warm, genuinely-helpful, safety-first. **Thank all first responders, emergency
management practitioners, and relief nonprofit organizations for the work they do in
communities when disaster strikes.** Place in: README (prominent), the skill text, a
`credits` command, and the `brief --markdown` footer.
**Disclaimer (with the thank-you):** unofficial tool; FEMA NSS / local emergency management
is authoritative; in a life-threatening emergency call 911 and follow official guidance and
evacuation orders. Shelter status updates ~2x/day and may lag reality.

## Fixture strategy (verified 2026-06-16 — Wayback has NO active-disaster query snapshot)
Checked Wayback CDX: only one tiny capture of the query URL (2025-08-24, ~2.9 KB = quiet). No large
active-event snapshot exists to replay. So:
- **Real smoke fixture (committed):** `fixtures/openshelters-live-2026-06-16.json` — the actual live
  feed (6 OPEN, all sparse: pets NONE, ADA/wheelchair UNK, lat/lon/population/capacity null). Proves
  the unknown-everything / empty-result paths against real data.
- **Synthetic active-disaster fixture (committed, LABELED SYNTHETIC):** hand-built using ONLY the real
  codes recovered above (pet COHABIT/ONSITE/NONE, status OPEN/FULL, real-format lat/lon, populated
  total_population + capacities, some null lat/lon to exercise geocoding). Header comment + filename
  (`*-SYNTHETIC.json`) state plainly it is fabricated test data, never presented as a real FEMA capture.
  Exercises `near` (distance + pet filter), `capacity` (utilization + FULL flag), and the `--pets/--ada/
  --wheelchair` filters. This is the only honest way to test active-event logic given no real snapshot.
- **Census geocoder** verified returning coords for a real shelter address (free, no key).

## Build approach (same hybrid as nhc)
1. Fixtures per the strategy above → `research/` docs + `fixtures/` (acceptance tests for near/capacity/filters).
2. `/printing-press` generates Go CLI + MCP + skills from a hand-authored internal YAML spec.
3. Hand-build the transcendence commands (near with geocoding, capacity, filters, brief, credits).
4. Adversarial code-review workflow → fix findings.
5. Publish: local folder + public repo `abe238/shelters-pp-cli` + PR to `mvanhorn/printing-press-library`.
