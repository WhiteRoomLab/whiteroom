package wpblock

import (
	"encoding/json/jsontext"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
)

type Kind uint8

const (
	Null Kind = iota
	Bool
	Number
	String
	Array
	Object
)

// Value is a JSON value. Only the field for Kind is set. Number is the number as it is written, such as 1.50 or 1e2.
// The members of an object are in the order of the text, and no name is twice in one.
type Value struct {
	Kind   Kind
	Bool   bool
	Number string
	String string
	Array  []Value
	Object []Member
}

type Member struct {
	Name  string
	Value Value
}

// MaxAttrsDepth is how deep arrays and objects nest in a value that is valid. PHP's json_decode with its default depth
// of 512 refuses 512 nested arrays or objects, empty ones too (PHP 8.5), and reads 511.
const MaxAttrsDepth = 511

var attrsOptions = jsontext.AllowDuplicateNames(true)

// DecodeAttrs reads s as PHP's json_decode(s, true) does, and reports false where that fails. A name that is repeated in
// an object keeps the place of its first appearance and the value of its last.
func DecodeAttrs(s string) (*Value, bool) {
	dec := jsontext.NewDecoder(strings.NewReader(s), attrsOptions)
	v, ok := readValue(dec)
	if !ok {
		return nil, false
	}
	// The decoder reads a stream of values, and the text must be one.
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return &v, true
}

// builder is an array or an object that is being read.
type builder struct {
	v       Value
	name    string
	hasName bool
	index   map[string]int // the places of the members, for an object that has many
}

const linearMembers = 16

func (b *builder) addMember(v Value) {
	name := b.name
	b.hasName = false
	obj := b.v.Object
	if b.index != nil {
		if i, ok := b.index[name]; ok {
			obj[i].Value = v
			return
		}
		b.index[name] = len(obj)
	} else {
		for i := range obj {
			if obj[i].Name == name {
				obj[i].Value = v
				return
			}
		}
		if len(obj) >= linearMembers {
			b.index = make(map[string]int, 2*linearMembers)
			for i := range obj {
				b.index[obj[i].Name] = i
			}
			b.index[name] = len(obj)
		}
	}
	b.v.Object = append(obj, Member{Name: name, Value: v})
}

func readValue(dec *jsontext.Decoder) (Value, bool) {
	var stack []builder
	for {
		tok, err := dec.ReadToken()
		if err != nil {
			return Value{}, false
		}
		var v Value
		switch tok.Kind() {
		case jsontext.KindBeginObject, jsontext.KindBeginArray:
			if len(stack) >= MaxAttrsDepth {
				return Value{}, false
			}
			kind := Object
			if tok.Kind() == jsontext.KindBeginArray {
				kind = Array
			}
			stack = append(stack, builder{v: Value{Kind: kind}})
			continue
		case jsontext.KindEndObject, jsontext.KindEndArray:
			v = stack[len(stack)-1].v
			stack = stack[:len(stack)-1]
		case jsontext.KindString:
			s := tok.String()
			if n := len(stack); n > 0 && stack[n-1].v.Kind == Object && !stack[n-1].hasName {
				stack[n-1].name, stack[n-1].hasName = s, true
				continue
			}
			v = Value{Kind: String, String: s}
		case jsontext.KindNumber:
			v = Value{Kind: Number, Number: tok.String()}
		case jsontext.KindTrue:
			v = Value{Kind: Bool, Bool: true}
		case jsontext.KindFalse:
			v = Value{Kind: Bool}
		case jsontext.KindNull:
			v = Value{Kind: Null}
		case jsontext.KindInvalid:
			return Value{}, false
		}
		n := len(stack)
		switch {
		case n == 0:
			return v, true
		case stack[n-1].v.Kind == Array:
			stack[n-1].v.Array = append(stack[n-1].v.Array, v)
		default:
			stack[n-1].addMember(v)
		}
	}
}

// Get returns the value of the member of an object. It returns nil if v is nil or not an object, or has no such member.
func (v *Value) Get(name string) *Value {
	if v == nil || v.Kind != Object {
		return nil
	}
	for i := range v.Object {
		if v.Object[i].Name == name {
			return &v.Object[i].Value
		}
	}
	return nil
}

func (v *Value) Str() (string, bool) {
	if v == nil || v.Kind != String {
		return "", false
	}
	return v.String, true
}

// IsTrue returns what a boolean is, and whether v is one.
func (v *Value) IsTrue() (value, ok bool) {
	if v == nil || v.Kind != Bool {
		return false, false
	}
	return v.Bool, true
}

// Float returns a number, unless it is too big for a float64.
func (v *Value) Float() (float64, bool) {
	if v == nil || v.Kind != Number {
		return 0, false
	}
	f, err := strconv.ParseFloat(v.Number, 64)
	if err != nil || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// Int returns a number that has no fraction, such as 3, 3.0, and 3e0, and is in the range of int64.
func (v *Value) Int() (int64, bool) {
	if v == nil || v.Kind != Number {
		return 0, false
	}
	return wholeNumber(v.Number)
}

// wholeNumber returns the int64 that the JSON number s is, if it is one.
func wholeNumber(s string) (int64, bool) {
	sign := ""
	if rest, ok := strings.CutPrefix(s, "-"); ok {
		sign, s = "-", rest
	}
	mantissa, exponent := s, ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa, exponent = s[:i], s[i+1:]
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return 0, true
	}
	// The value is digits times 10 to the power of shift.
	shift := -len(fraction)
	if exponent != "" {
		e, err := strconv.ParseInt(exponent, 10, 32)
		if err != nil {
			return 0, false
		}
		shift += int(e)
	}
	if shift < 0 {
		cut := len(digits) + shift
		if cut < 0 || strings.Trim(digits[cut:], "0") != "" {
			return 0, false
		}
		digits, shift = digits[:cut], 0
	}
	if len(digits)+shift > len("9223372036854775807") {
		return 0, false
	}
	n, err := strconv.ParseInt(sign+digits+strings.Repeat("0", shift), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
