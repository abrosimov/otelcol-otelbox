// A module of its own, deliberately not at the repository root: this repository
// builds its locally adapted exporters as separate modules, and a root go.mod
// would collide with the one OCB generates into _build/ at build time.
//
// It has no dependencies and must keep none. Stdlib-only means no go.sum, so
// nothing here has to be downloaded, cached or vendored before CI can run the
// harness against a binary it has already built.
module github.com/abrosimov/otelcol-otelbox/test/harness

go 1.27.0
