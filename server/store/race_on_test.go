//go:build race

package store

// raceEnabled is true under -race, where the transpiled SQLite runs ~25x
// slower; the load test then only logs its timing. `make test` also runs the
// load test without -race, where the 1s budget is enforced.
const raceEnabled = true
