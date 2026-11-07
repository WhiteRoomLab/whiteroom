//go:build !race

package edit

// raceEnabled reports whether the tests run with -race, which makes them several times slower.
const raceEnabled = false
