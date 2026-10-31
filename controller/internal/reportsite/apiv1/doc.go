// Package apiv1 holds the JSON types of the reporting site API, served under
// /api/v1/ by the controller (see CONTRACT.md, "Reporting site"). The types
// are the source of truth: ui/src/api/types.ts mirrors them by hand, and the
// fixtures in ui/src/api/fixtures/ must decode into them without unknown
// fields (fixtures_test.go).
//
// The package is named after the URL version so that a later /api/v2/ can sit
// next to it, and so that it is not confused with the CRD module
// github.com/dirien/skill-ci/controller/api.
//
// JSON rules, which the TypeScript mirror relies on:
//   - field names are lowerCamelCase;
//   - lists are always present, an empty list is [] and never null;
//   - an optional value (omitempty) is left out when unknown, never null;
//   - null appears only inside the cell lists of the skill matrix
//     (VersionRow.Cells) and the trial grid (GridCase.WithSkill,
//     GridCase.WithoutSkill), where it marks a missing cell;
//   - times are RFC 3339 strings in UTC.
package apiv1
