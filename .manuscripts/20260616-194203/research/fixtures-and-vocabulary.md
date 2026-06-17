# shelters-pp-cli test fixtures

Two fixtures, with a deliberate and important distinction:

## `openshelters-live-2026-06-16.json` — REAL
The actual FEMA OpenShelters query response captured live on 2026-06-16
(`FeatureServer/0/query?where=1=1&outFields=*&returnGeometry=false&f=json`).
6 OPEN shelters, all sparse: `pet_accommodations_code=NONE`, `ada_compliant=UNK`,
`wheelchair_accessible=UNK`, and `latitude/longitude/total_population/
evacuation_capacity/post_impact_capacity` all null. This proves the
unknown-everything and empty-result paths against genuine data.

## `openshelters-active-SYNTHETIC.json` — FABRICATED (NOT a real capture)
Hand-built by `build_synthetic.py`. **This is not real FEMA data and must never be
presented as one.** No active-event snapshot of the OpenShelters query endpoint
exists to replay (Wayback holds only one tiny quiet capture), so this synthetic
file exists solely to exercise logic that the quiet live feed cannot:
`near` (haversine + geocoding), `capacity` (utilization + reported-FULL), and the
`--pets/--ada/--wheelchair` filters.

Every coded value uses the **real** vocabulary recovered from FEMA's own FEMA_NSS
filtered layers (verified 2026-06-16): `pet_accommodations_code` in
`{NONE, COHABIT, ONSITE}`, `ada_compliant`/`wheelchair_accessible` in
`{YES, NO, UNK}`, `shelter_status` in `{OPEN, FULL}`. Shelter names carry an
`(EXERCISE)` marker, the JSON carries a top-level `_synthetic_notice`, and the
filename says `SYNTHETIC`.

12 shelters: 10 OPEN + 2 FULL; 8 with coordinates (deterministic distance tests)
and 4 with null coordinates + real street addresses (geocoding / skip-with-count
path); 10 with computable utilization (one has a null `evacuation_capacity` so its
% must fall back to `post_impact_capacity` and be labeled as such).

## Distance oracle (independent ground truth)
The haversine implementation is checked against the canonical published vector
`(36.12, -86.67) -> (33.94, -118.40) = 2887.2599 km` (≈1793.56 mi, mean Earth
radius 3958.7613 mi), not against numbers derived from the fixture itself.
