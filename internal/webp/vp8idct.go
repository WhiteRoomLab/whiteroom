package webp

// inverseWHT inverts the Walsh-Hadamard transform of a Y2 block (RFC 6386 section 14.3) into the DC coefficients of
// the 16 Y blocks in coeffs.
func inverseWHT(in *[16]int16, coeffs *[25 * 16]int16) {
	var t [16]int
	for i := range 4 {
		a := int(in[i]) + int(in[12+i])
		b := int(in[4+i]) + int(in[8+i])
		c := int(in[4+i]) - int(in[8+i])
		d := int(in[i]) - int(in[12+i])
		t[i] = a + b
		t[4+i] = c + d
		t[8+i] = a - b
		t[12+i] = d - c
	}
	for i := range 4 {
		r := t[4*i : 4*i+4 : 4*i+4]
		a := r[0] + r[3]
		b := r[1] + r[2]
		c := r[1] - r[2]
		d := r[0] - r[3]
		coeffs[16*(4*i)] = int16((a + b + 3) >> 3)
		coeffs[16*(4*i+1)] = int16((c + d + 3) >> 3)
		coeffs[16*(4*i+2)] = int16((a - b + 3) >> 3)
		coeffs[16*(4*i+3)] = int16((d - c + 3) >> 3)
	}
}

// The constants of the inverse DCT: sqrt(2)*cos(pi/8) - 1 and sqrt(2)*sin(pi/8) in 16-bit fixed point.
const (
	idctC1 = 20091
	idctS1 = 35468
)

// idctAdd adds the inverse DCT of in (RFC 6386 section 14.4) to the 4x4 pixels at b[o:], whose rows are stride apart.
func idctAdd(in *[16]int16, b []byte, o, stride int) {
	var t [16]int
	for i := range 4 {
		i0, i4, i8, i12 := int(in[i]), int(in[4+i]), int(in[8+i]), int(in[12+i])
		a := i0 + i8
		bb := i0 - i8
		c := i4*idctS1>>16 - (i12 + i12*idctC1>>16)
		d := i4 + i4*idctC1>>16 + i12*idctS1>>16
		t[i] = a + d
		t[4+i] = bb + c
		t[8+i] = bb - c
		t[12+i] = a - d
	}
	for i := range 4 {
		r := t[4*i : 4*i+4 : 4*i+4]
		a := r[0] + r[2]
		bb := r[0] - r[2]
		c := r[1]*idctS1>>16 - (r[3] + r[3]*idctC1>>16)
		d := r[1] + r[1]*idctC1>>16 + r[3]*idctS1>>16
		p := b[o : o+4 : o+4]
		p[0] = clip8(int(p[0]) + (a+d+4)>>3)
		p[1] = clip8(int(p[1]) + (bb+c+4)>>3)
		p[2] = clip8(int(p[2]) + (bb-c+4)>>3)
		p[3] = clip8(int(p[3]) + (a-d+4)>>3)
		o += stride
	}
}

// dcAdd adds the inverse DCT of a block whose only non-zero coefficient is dc: the same for every pixel.
func dcAdd(dc int16, b []byte, o, stride int) {
	v := (int(dc) + 4) >> 3
	for range 4 {
		p := b[o : o+4 : o+4]
		p[0] = clip8(int(p[0]) + v)
		p[1] = clip8(int(p[1]) + v)
		p[2] = clip8(int(p[2]) + v)
		p[3] = clip8(int(p[3]) + v)
		o += stride
	}
}

func clip8(v int) byte {
	return byte(min(max(v, 0), 255))
}
