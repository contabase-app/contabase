// Package civildateplan provides the pure contract for civil date repair plans.
//
// V2A: private structural parser and schema v1 validation. No exported operational API.
//
// V2C: snapshots and the canonical TLV digest
// (civil-date-repair-plan-digest/v1) are implemented.
// AuthorizedRepairPlan stores [32]byte digest computed from the canonical state.
// Method Digest() returns "sha256:" + hex. Encoder and compute function are private.
// Golden vectors and field coverage tests freeze the byte-level contract.
// V2D-A adds only private in-memory preparation from untrusted bytes to an
// independent validated state plus its calculated canonical digest. It does
// not create an AuthorizedRepairPlan or confer authorization.
// No file walker, loader, or external digest comparison exists yet.
//
// AuthorizedRepairPlan is an opaque handle. Copying the struct by value copies
// the seal and the state pointer. Copies share the same underlying
// authorizedPlanState, which is safe because the state has no mutable public
// API. All getters return snapshots and optionals by value. No internal slices
// or references escape. There is no Clone method at this phase.
//
// V2D-C: LoadAuthorizedRepairPlan reads a repair plan from path, prepares it
// structurally and semantically via readPlanFileBytes → preparePlanBytes, validates
// an approved digest with strict parsing (exactly "sha256:" + 64 lowercase hex),
// compares against the calculated semantic digest, and returns the only
// authorized construction of *AuthorizedRepairPlan in the package.
// Capability is nil for every error.
//
// Plan and apply remain fail-closed via the admin CLI.
package civildateplan
