package logdb

import (
	"fmt"
	"math"
)

// toInt64, toInt and toUint32 return v, a number from a file, in a smaller type, or false if it does not fit.
func toInt64(v uint64) (int64, bool) {
	if v > math.MaxInt64 {
		return 0, false
	}
	return int64(v), true
}

func toInt(v uint64) (int, bool) {
	if v > math.MaxInt {
		return 0, false
	}
	return int(v), true
}

func toUint32(v uint64) (uint32, bool) {
	if v > math.MaxUint32 {
		return 0, false
	}
	return uint32(v), true
}

// fromInt and fromInt64 return v, a count, a size or an offset of the engine, as a uint64. They panic if v is
// negative, as the encoders do on what could not be read again.
func fromInt(v int) uint64 {
	if v < 0 {
		panic(fmt.Sprintf("logdb: negative number %d", v))
	}
	return uint64(v)
}

func fromInt64(v int64) uint64 {
	if v < 0 {
		panic(fmt.Sprintf("logdb: negative number %d", v))
	}
	return uint64(v)
}
