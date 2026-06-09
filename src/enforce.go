// enforce.go is now integrated into laozi.go.
// This file is intentionally empty — all enforcement logic lives in laozi.go
// alongside the types and pipeline it depends on, keeping the package as a
// single coherent unit.
//
// The enforcement functions integrated into laozi.go:
//   - computeAnalysis()    — deterministic ground truth computation
//   - enforce()            — post-LLM reconciliation (severity, reference, goal, numbers)
//   - referenceMatches()   — citation validation
//   - enforceGoal()        — suggested goal correction
//   - renderText()         — deterministic prose fallback
//   - unknownNumbers()     — heuristic number tracing in prose
//   - WithStrict()         — strict mode option
package laozi
