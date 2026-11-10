//go:build !race

package wpformat

// raceEnabled reports whether the tests run with -race, which makes them several times slower.
const raceEnabled = false
