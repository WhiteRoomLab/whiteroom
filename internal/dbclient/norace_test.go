//go:build !race

package dbclient_test

// raceEnabled reports whether the tests run with -race, which makes them several times slower.
const raceEnabled = false
