// Package engine hosts private resolution, thread coordination, profile, and
// runtime-service operations used by the public Agent pipeline.
//
// Layering rules:
//
//   - engine MUST NOT import the root package.
//   - Driver-facing contracts live in package driver; application vocabulary
//     lives in public leaf packages.
//   - The root package owns orchestration and calls these operations through
//     narrow free-function boundaries.
package engine
