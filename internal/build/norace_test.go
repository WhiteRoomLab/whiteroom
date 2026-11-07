//go:build !race

package build

// raceEnabled reports whether the tests run with -race, which makes them several times slower.
const raceEnabled = false
