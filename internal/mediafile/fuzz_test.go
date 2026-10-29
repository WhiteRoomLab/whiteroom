package mediafile

import (
	"bytes"
	"slices"
	"testing"
)

func FuzzProbe(f *testing.F) {
	for _, tt := range testFiles(f) {
		f.Add(tt.data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := Probe(bytes.NewReader(data))
		switch {
		case err != nil:
			if got != (Info{}) {
				t.Errorf("Probe() = %+v, %v; want a zero Info with an error", got, err)
			}
		case !slices.Contains(Formats(), got.MIMEType):
			t.Errorf("Probe() = %+v; the MIME type is not in Formats", got)
		case got.MIMEType == PDF:
			if got != (Info{MIMEType: PDF}) {
				t.Errorf("Probe() = %+v; want 0 for the size and the orientation of PDF", got)
			}
		case got.Width < 1 || got.Height < 1 || got.Orientation < 1 || got.Orientation > 8:
			t.Errorf("Probe() = %+v; want a size of 1 or more and an orientation from 1 to 8", got)
		}
	})
}
