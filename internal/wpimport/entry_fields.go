package wpimport

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/phpserialize"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// keyRules tell which postmeta keys are custom fields (6.10.5), as opposed to what WordPress, the theme and the plugins keep
// of their own: the keys that start with "_", those of Rank Math and SEO SIMPLE PACK, and those of the SEO plugin that the
// settings name. A key that the table of the type has is a custom field, whatever it is.
type keyRules struct {
	plugin map[string]bool   // the keys of the SEO plugin of the settings
	table  map[string]string // fields of the settings for the type: the key and the name of the field
}

// pluginKeys returns the postmeta keys of the SEO plugin called name, or nil if there is none.
func pluginKeys(name string) map[string]bool {
	p := seoPluginNamed(name)
	if p == nil {
		return nil
	}
	set := make(map[string]bool)
	for _, k := range p.keys() {
		set[k] = true
	}
	return set
}

func (r keyRules) custom(key string) bool {
	if _, ok := r.table[key]; ok {
		return true
	}
	return !strings.HasPrefix(key, "_") && !strings.HasPrefix(key, "rank_math_") && !strings.HasPrefix(key, "ssp_meta_") && !r.plugin[key]
}

// candidates returns the custom fields of the item that have a value, each once, in the order of the postmeta, except the
// keys in skip and the keys of the rows of a list: <parent>_<number>_<rest>, where the parent is also a key of the item.
func (r keyRules) candidates(it *wxr.Item, skip map[string]bool, idx *metaIndex) []string {
	var keys []string
	var seen map[string]bool
	for i := range it.Meta {
		m := &it.Meta[i]
		if skip[m.Key] || seen[m.Key] || !r.custom(m.Key) || strings.TrimSpace(m.Value) == "" {
			continue
		}
		if seen == nil {
			seen = make(map[string]bool)
		}
		seen[m.Key] = true
		if !isRowKey(m.Key, idx) {
			keys = append(keys, m.Key)
		}
	}
	return keys
}

// nestedKeys are the keys that the fields in the groups and in the rows of the lists of a post type are read from, when each
// field of the type is read from the key of its name. The table of the settings does not map them, so ProposeSettings does not
// list them.
type nestedKeys struct {
	fields map[string]bool // the keys of the fields in the groups
	lists  map[string]bool // the keys of the lists of groups: their rows have the keys <key>_<number>_<name>
}

func nestedKeysOf(def *content.TypeDef) *nestedKeys {
	n := &nestedKeys{fields: make(map[string]bool), lists: make(map[string]bool)}
	if def != nil {
		for i := range def.Fields {
			n.add(&def.Fields[i], def.Fields[i].Name, true)
		}
	}
	return n
}

func (n *nestedKeys) add(f *content.FieldDef, key string, top bool) {
	switch {
	case f.Type == content.FieldGroup:
		for i := range f.Fields {
			n.add(&f.Fields[i], key+"_"+f.Fields[i].Name, false)
		}
		return
	case f.Type == content.FieldList && f.Of != nil && f.Of.Type == content.FieldGroup:
		n.lists[key] = true
	}
	if !top {
		n.fields[key] = true
	}
}

func (n *nestedKeys) has(key string) bool {
	if n.fields[key] {
		return true
	}
	for sp := range rowSplits(key) {
		if n.lists[key[:sp.i]] {
			return true
		}
	}
	return false
}

// rowSplit is a place in a key that has the form _<number>_.
type rowSplit struct {
	i    int // the index of the first "_"
	rest int // the index after the second
	n    int
}

// rowNumber reads the number of a row and the "_" after it at the start of s, and returns the number and the index after
// the "_". The number is one that a row has: its digits, up to nine, have no leading 0.
func rowNumber(s string) (n, rest int, ok bool) {
	j := 0
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == 0 || j >= len(s) || s[j] != '_' || j > 9 || s[0] == '0' && j > 1 {
		return 0, 0, false
	}
	n, _ = strconv.Atoi(s[:j])
	return n, j + 1, true
}

// rowSplits returns the places in key that have the form _<number>_, where the number is one that rowNumber reads. The
// first character of key is never the "_".
func rowSplits(key string) iter.Seq[rowSplit] {
	return func(yield func(rowSplit) bool) {
		for i := 1; i < len(key); i++ {
			if key[i] != '_' {
				continue
			}
			n, rest, ok := rowNumber(key[i+1:])
			if ok && !yield(rowSplit{i: i, rest: i + 1 + rest, n: n}) {
				return
			}
		}
	}
}

// isRowKey reports whether key is <parent>_<number>_<rest>, where the parent is a key of the postmeta in idx.
func isRowKey(key string, idx *metaIndex) bool {
	for sp := range rowSplits(key) {
		if idx.has(key[:sp.i]) {
			return true
		}
	}
	return false
}

// metaIndex finds the postmeta of an item by key, and those that are the rows of a list by the key of the list. Each is made
// of all the postmeta once, when it is first needed.
type metaIndex struct {
	metas wxr.MetaList
	first map[string]int32 // the index of the first postmeta with the key
	next  []int32          // the index of the next postmeta with the key of the one at the index, or -1
	// sorted are the indexes of the postmeta in the order of their keys: the keys that start with the key of a list are next
	// to each other. It takes four bytes for each postmeta, however many places like those of rows a key has.
	sorted []int32
}

// rowKey is a postmeta whose key is <prefix><number>_<rest>.
type rowKey struct {
	n    int
	meta int32 // the index of the postmeta
	rest int32 // where <rest> starts in the key
}

func (x *metaIndex) build() {
	if x.first != nil {
		return
	}
	x.first = make(map[string]int32, len(x.metas))
	x.next = make([]int32, len(x.metas))
	for i := len(x.metas) - 1; i >= 0; i-- {
		key := x.metas[i].Key
		if j, ok := x.first[key]; ok {
			x.next[i] = j
		} else {
			x.next[i] = -1
		}
		x.first[key] = int32(i)
	}
}

func (x *metaIndex) has(key string) bool {
	x.build()
	_, ok := x.first[key]
	return ok
}

// values returns the indexes of the postmeta with the key that have a value, in order.
func (x *metaIndex) values(key string) []int32 {
	x.build()
	i, ok := x.first[key]
	if !ok {
		return nil
	}
	var at []int32
	for ; i >= 0; i = x.next[i] {
		if strings.TrimSpace(x.metas[i].Value) != "" {
			at = append(at, i)
		}
	}
	return at
}

// rowsOf returns the keys of the rows of the list whose key is prefix without its "_": those of the form
// <prefix><number>_<rest>, in the order of their numbers, and then of the postmeta.
func (x *metaIndex) rowsOf(prefix string) []rowKey {
	if x.sorted == nil {
		x.sorted = make([]int32, len(x.metas))
		for i := range x.sorted {
			x.sorted[i] = int32(i)
		}
		slices.SortFunc(x.sorted, func(a, b int32) int { return strings.Compare(x.metas[a].Key, x.metas[b].Key) })
	}
	at, _ := slices.BinarySearchFunc(x.sorted, prefix, func(i int32, p string) int { return strings.Compare(x.metas[i].Key, p) })
	var rows []rowKey
	for _, i := range x.sorted[at:] {
		after, ok := strings.CutPrefix(x.metas[i].Key, prefix)
		if !ok {
			break
		}
		if n, rest, ok := rowNumber(after); ok {
			rows = append(rows, rowKey{n: n, meta: i, rest: int32(len(prefix) + rest)})
		}
	}
	slices.SortFunc(rows, func(a, b rowKey) int { return cmp.Or(cmp.Compare(a.n, b.n), cmp.Compare(a.meta, b.meta)) })
	return rows
}

// typeFields is what the settings say about the custom fields of a post type.
type typeFields struct {
	rules keyRules
	keys  []string // the key of each field of the type, or "" for none
}

// unreadFields counts the custom fields of the items of a post type that were not imported.
type unreadFields struct {
	typ    string
	first  *wxr.Item // the first item that has one
	counts map[string]int
}

// fieldState is what setFields keeps between the items.
type fieldState struct {
	plugin     map[string]bool
	pluginDone bool
	types      map[string]*typeFields
	unread     []*unreadFields // in the order of the first item of each type
}

// fieldKeys finds how the custom fields of the post type typ are read: each field of its definition is read from the key
// that the table of the settings maps to it, or else from the key of its own name, unless the table has that key for
// another field, or for none.
func (c *entryConv) fieldKeys(typ string, def *content.TypeDef) *typeFields {
	if tf := c.fields.types[typ]; tf != nil {
		return tf
	}
	if !c.fields.pluginDone {
		c.fields.pluginDone = true
		if c.opts.Settings != nil {
			c.fields.plugin = pluginKeys(c.opts.Settings.SEO.Plugin)
		}
	}
	var table map[string]string
	if c.opts.Settings != nil {
		table = c.opts.Settings.Fields[typ]
	}
	tf := &typeFields{rules: keyRules{plugin: c.fields.plugin, table: table}, keys: make([]string, len(def.Fields))}
	// Site makes sure that no two keys have one field, so the first of them, in order, is only a choice for what it did not check.
	keyOf := make(map[string]string, len(table))
	for _, key := range slices.Sorted(maps.Keys(table)) {
		if name := table[key]; name != "" && keyOf[name] == "" {
			keyOf[name] = key
		}
	}
	for i := range def.Fields {
		name := def.Fields[i].Name
		if key, ok := keyOf[name]; ok {
			tf.keys[i] = key
		} else if _, mapped := table[name]; !mapped && tf.rules.custom(name) {
			tf.keys[i] = name
		}
	}
	if c.fields.types == nil {
		c.fields.types = make(map[string]*typeFields)
	}
	c.fields.types[typ] = tf
	return tf
}

// setFields makes the fields of the revision of ei of the custom fields of the item: the postmeta of the keys that the
// settings, or the names of the fields, say. It counts the custom fields that it does not read, for reportFields.
func (c *entryConv) setFields(ei *entryItem) {
	if len(ei.it.Meta) == 0 {
		return
	}
	def := c.site.Type(ei.entry.Type)
	if def == nil {
		return
	}
	tf := c.fieldKeys(ei.entry.Type, def)
	x := &fieldConv{c: c, ei: ei, rules: tf.rules, index: metaIndex{metas: ei.it.Meta}}
	for i := range def.Fields {
		key := tf.keys[i]
		if key == "" {
			continue
		}
		f := &def.Fields[i]
		x.top, x.topKey, x.replaced = "/fields/"+pointerEscaper.Replace(f.Name), key, false
		if v := x.value(f, key); v != nil {
			if ei.rev.Fields == nil {
				ei.rev.Fields = make(content.Fields)
			}
			ei.rev.Fields[f.Name] = v
		}
		x.reportUnknown(key)
	}
	if keys := x.rules.candidates(ei.it, x.handled, &x.index); len(keys) > 0 {
		u := c.unreadOf(ei)
		for _, key := range keys {
			u.counts[key]++
		}
	}
}

// unreadOf returns the count of the items of the type of ei, which has an item with a custom field that is not imported now.
func (c *entryConv) unreadOf(ei *entryItem) *unreadFields {
	for _, u := range c.fields.unread {
		if u.typ == ei.entry.Type {
			return u
		}
	}
	u := &unreadFields{typ: ei.entry.Type, first: ei.it, counts: make(map[string]int)}
	c.fields.unread = append(c.fields.unread, u)
	return u
}

// reportFields records the custom fields that are not imported, once for each post type, at its first item.
func (c *entryConv) reportFields() {
	for _, u := range c.fields.unread {
		src := Source{File: c.imp.File, Line: u.first.Line, Column: u.first.Column, Record: recordItem, Type: u.first.Type}
		typ, list := typeText(u.typ), usedNamesOf(u.counts, "item", "items")
		msg := fmt.Sprintf("the custom fields %s of the post type %s are not imported; fields.%s of the settings maps them to the fields of the schema file",
			list, typ, typ)
		if len(u.counts) == 1 {
			msg = fmt.Sprintf("the custom field %s of the post type %s is not imported; fields.%s of the settings maps it to a field of the schema file",
				list, typ, typ)
		}
		c.imp.Report.Add(Entry{Kind: Skipped, Source: src, Message: msg})
	}
	c.fields.unread = nil
}

// fieldConv converts the custom fields of one item.
type fieldConv struct {
	c     *entryConv
	ei    *entryItem
	rules keyRules
	index metaIndex
	// handled are the keys of the postmeta that were read or recorded: they are not counted as fields that are not imported.
	handled map[string]bool
	// top is the JSON Pointer of the field of the revision that is being made, topKey the key that it is made of, and m the
	// postmeta that is being converted: the records are about them. replaced is set when the report says that line breaks of
	// the field are made spaces, which it says once.
	top, topKey string
	m           wxr.Meta
	replaced    bool
	// unknown are the names, after <key>_<number>_ in the key, of the sub fields of the rows that the schema file does not
	// have, and where the first is.
	unknown     []string
	unknownSeen map[string]bool
	unknownMeta wxr.Meta
	// fails are the values of the field being converted that are not imported, and values how many it had.
	fails  []itemFailure
	values int
}

// itemFailure is a value of a field with many values that is not imported, and why.
type itemFailure struct {
	n   int
	why string
	m   wxr.Meta
}

// fieldItem is a value of a field, and the postmeta that it is in.
type fieldItem struct {
	v phpserialize.Value
	m wxr.Meta
}

func (x *fieldConv) note(kind Kind, format string, args ...any) {
	x.c.metaNote(kind, x.ei, x.m, x.top, format, args...)
}

func (x *fieldConv) handle(key string) {
	if x.handled == nil {
		x.handled = make(map[string]bool)
	}
	x.handled[key] = true
}

// isMulti reports whether f has a list of values.
func isMulti(f *content.FieldDef) bool {
	return f.Type == content.FieldList || f.Multiple && (f.Type == content.FieldSelect || f.Type == content.FieldEntry || f.Type == content.FieldTerm)
}

// value makes the value of the field f from the postmeta of the key, or of the keys that start with it. It returns nil if
// there is none.
func (x *fieldConv) value(f *content.FieldDef, key string) jsontext.Value {
	switch {
	case f.Type == content.FieldGroup:
		return x.group(f, key)
	case f.Type == content.FieldList && f.Of != nil && f.Of.Type == content.FieldGroup:
		return x.rowList(f, key)
	}
	return x.leaf(f, key)
}

// group makes the object of the fields of the group g, which are in the keys <base>_<name>.
func (x *fieldConv) group(g *content.FieldDef, base string) jsontext.Value {
	var obj map[string]jsontext.Value
	for i := range g.Fields {
		child := &g.Fields[i]
		if v := x.value(child, base+"_"+child.Name); v != nil {
			if obj == nil {
				obj = make(map[string]jsontext.Value)
			}
			obj[child.Name] = v
		}
	}
	if obj == nil {
		return nil
	}
	return jsonValue(obj)
}

// rowList makes the rows of the list f of groups (the repeater of ACF), which are in the keys <key>_<number>_<name>. The
// numbers are found in the keys, not by the number that the postmeta of the key has: the rows in the order of the numbers, and
// no row that has no value.
func (x *fieldConv) rowList(f *content.FieldDef, key string) jsontext.Value {
	x.handle(key)
	prefix := key + "_"
	keys := x.index.rowsOf(prefix)
	var rows []jsontext.Value
	for i := 0; i < len(keys); {
		n, j := keys[i].n, i
		for j < len(keys) && keys[j].n == n {
			j++
		}
		if row := x.group(f.Of, prefix+strconv.Itoa(n)); row != nil {
			rows = append(rows, row)
		}
		i = j
	}
	// What the rows have that no field is for.
	for _, k := range keys {
		m := x.index.metas[k.meta]
		if x.handled[m.Key] || strings.TrimSpace(m.Value) == "" {
			continue
		}
		x.handle(m.Key)
		name := m.Key[k.rest:]
		if x.unknownSeen[name] {
			continue
		}
		if x.unknownSeen == nil {
			x.unknownSeen = make(map[string]bool)
		}
		x.unknownSeen[name] = true
		if len(x.unknown) == 0 {
			x.unknownMeta = m
		}
		x.unknown = append(x.unknown, name)
	}
	return jsonArray(rows)
}

// reportUnknown records the sub fields that rowList found no field for in the field that was made of key.
func (x *fieldConv) reportUnknown(key string) {
	if len(x.unknown) == 0 {
		return
	}
	x.m = x.unknownMeta
	names := x.unknown[:min(len(x.unknown), maxLostShown)]
	list := make([]string, len(names))
	for i, name := range names {
		list[i] = strconv.Quote(name)
	}
	text := strings.Join(list, ", ")
	if more := len(x.unknown) - len(names); more > 0 {
		text += fmt.Sprintf(", and %d more", more)
	}
	if len(x.unknown) == 1 {
		x.note(Skipped, "the sub field %s of the custom field %q is not imported; the field has no such field in the schema file", text, key)
	} else {
		x.note(Skipped, "the sub fields %s of the custom field %q are not imported; the field has no such fields in the schema file", text, key)
	}
	x.unknown, x.unknownSeen = nil, nil
}

// leaf makes the value of f, a field that is not a group or a list of groups, from the postmeta with the key. If f has one
// value, the first that is not empty is it.
func (x *fieldConv) leaf(f *content.FieldDef, key string) jsontext.Value {
	at := x.index.values(key)
	if len(at) == 0 {
		return nil
	}
	x.handle(key)
	if !isMulti(f) {
		x.m = x.index.metas[at[0]]
		if len(at) > 1 {
			x.note(Skipped, "the custom field %q has %d values; the first is imported", key, len(at))
		}
		v, ok := x.parse(x.m)
		if !ok || blankValue(v) {
			return nil
		}
		out, why := x.one(f, v)
		if why != "" {
			x.note(Skipped, "the custom field %q is not imported: %s", key, why)
		}
		return out
	}
	items := make([]fieldItem, 0, len(at))
	for _, i := range at {
		m := x.index.metas[i]
		if v, ok := x.parse(m); ok {
			items = append(items, fieldItem{v, m})
		}
	}
	x.fails, x.values = x.fails[:0], 0
	out := x.many(f, items)
	x.reportFailures(key)
	return out
}

// parse reads the value of the postmeta m as WordPress does, and records a value that cannot be read.
func (x *fieldConv) parse(m wxr.Meta) (phpserialize.Value, bool) {
	v, err := Unserialize(x.c.imp.Report, ItemSource(x.c.imp.File, x.ei.it), m)
	if err != nil {
		x.m = m
		msg := err.Error()
		var ce *content.Error
		if errors.As(err, &ce) {
			msg = ce.Msg
		}
		x.note(Skipped, "the custom field %q is not imported: %s", m.Key, msg)
		return phpserialize.Value{}, false
	}
	return v, true
}

// many makes the array of the values of f, a list or a field with multiple values, from items: the elements of the array that
// is the only item, or else the items. Those that are not imported are put in x.fails.
func (x *fieldConv) many(f *content.FieldDef, items []fieldItem) jsontext.Value {
	elem := f.Of
	if f.Type != content.FieldList {
		one := *f
		one.Multiple = false
		elem = &one
	}
	if elem == nil {
		return nil
	}
	if len(items) == 1 && isPHPList(items[0].v) {
		array := items[0]
		items = make([]fieldItem, len(array.v.Array))
		for i, e := range array.v.Array {
			items[i] = fieldItem{e.Value, array.m}
		}
	}
	var out []jsontext.Value
	var seen map[string]bool
	for _, it := range items {
		if blankValue(it.v) {
			continue
		}
		x.m = it.m
		if !isMulti(elem) {
			x.values++
		}
		v, why := x.one(elem, it.v)
		switch {
		case why != "":
			x.fails = append(x.fails, itemFailure{n: x.values, why: why, m: it.m})
		case v == nil:
		case f.Type != content.FieldList && seen[string(v)]:
		default:
			if seen == nil {
				seen = make(map[string]bool)
			}
			seen[string(v)] = true
			out = append(out, v)
		}
	}
	return jsonArray(out)
}

// reportFailures records the values of the field with the key that are not imported, once.
func (x *fieldConv) reportFailures(key string) {
	n := len(x.fails)
	if n == 0 {
		return
	}
	x.m = x.fails[0].m
	if x.values == 1 {
		x.note(Skipped, "the custom field %q is not imported: %s", key, x.fails[0].why)
		return
	}
	shown := x.fails[:min(n, maxLostShown)]
	parts := make([]string, len(shown))
	for i, f := range shown {
		parts[i] = fmt.Sprintf("value %d: %s", f.n, f.why)
	}
	list := strings.Join(parts, "; ")
	if more := n - len(shown); more > 0 {
		list += fmt.Sprintf("; and %d more", more)
	}
	verb := "are"
	if n == 1 {
		verb = "is"
	}
	x.note(Skipped, "%d of the %d values of the custom field %q %s not imported: %s", n, x.values, key, verb, list)
}

// isPHPList reports whether v is a PHP array whose keys are numbers, such as the IDs that a relationship has, and not one
// whose keys are names, such as a link.
func isPHPList(v phpserialize.Value) bool {
	return v.Kind == phpserialize.Array && !slices.ContainsFunc(v.Array, func(e phpserialize.Entry) bool { return e.Key.Kind != phpserialize.Int })
}

// blankValue reports whether v is a value that WordPress has for no value: nothing, an empty list, and text that is only
// white space.
func blankValue(v phpserialize.Value) bool {
	switch v.Kind {
	case phpserialize.Null:
		return true
	case phpserialize.Bool:
		return !v.Bool
	case phpserialize.String:
		return strings.TrimSpace(v.Str) == ""
	case phpserialize.Array:
		return len(v.Array) == 0
	case phpserialize.Int, phpserialize.Float:
	}
	return false
}

// phpText returns the text that PHP makes of a value that is not a list.
func phpText(v phpserialize.Value) (string, bool) {
	switch v.Kind {
	case phpserialize.String:
		return v.Str, true
	case phpserialize.Int:
		return strconv.FormatInt(v.Int, 10), true
	case phpserialize.Float:
		if math.IsNaN(v.Float) || math.IsInf(v.Float, 0) {
			return "", false
		}
		return strconv.FormatFloat(v.Float, 'f', -1, 64), true
	case phpserialize.Bool:
		if v.Bool {
			return "1", true
		}
		return "", true
	case phpserialize.Null:
		return "", true
	case phpserialize.Array:
	}
	return "", false
}

// one makes the value of f, a field of one value, from v, or says why there is none. It returns no value and no reason for
// a value that means that there is none, such as false.
func (x *fieldConv) one(f *content.FieldDef, v phpserialize.Value) (jsontext.Value, string) {
	if isMulti(f) {
		return x.many(f, []fieldItem{{v, x.m}}), ""
	}
	switch f.Type {
	case content.FieldLink:
		return x.link(v)
	case content.FieldRichText, content.FieldBlocks:
		return nil, "the import does not convert HTML into richText and blocks fields"
	case content.FieldGroup, content.FieldList:
		return nil, "the import does not read a group in a list of values"
	case content.FieldText, content.FieldTextarea, content.FieldNumber, content.FieldBoolean, content.FieldDate,
		content.FieldDatetime, content.FieldSelect, content.FieldMedia, content.FieldEntry, content.FieldTerm:
	}
	if v.Kind == phpserialize.Float && (math.IsNaN(v.Float) || math.IsInf(v.Float, 0)) {
		return nil, "it is not a finite number"
	}
	text, ok := phpText(v)
	if !ok {
		return nil, "it is a list, not one value"
	}
	switch f.Type {
	case content.FieldText:
		line, replaced := oneLine(text)
		out, why := stringValue(line)
		if why == "" && replaced && !x.replaced {
			x.replaced = true
			x.note(Changed, "line breaks and tabs in the custom field %q are spaces here", x.topKey)
		}
		return out, why
	case content.FieldTextarea:
		return stringValue(text)
	case content.FieldSelect:
		return stringValue(strings.TrimSpace(text))
	case content.FieldNumber:
		return numberValue(text)
	case content.FieldBoolean:
		return boolValue(text)
	case content.FieldDate:
		return dateValue(text)
	case content.FieldDatetime:
		return x.datetimeValue(text)
	case content.FieldMedia:
		return x.media(f, text)
	case content.FieldEntry:
		return x.entry(f, text)
	case content.FieldTerm:
		return x.term(f, text)
	case content.FieldRichText, content.FieldBlocks, content.FieldLink, content.FieldGroup, content.FieldList:
	}
	return nil, "the import does not read this kind of field"
}

// jsonValue returns v as JSON, as the content package writes the values of fields.
func jsonValue(v any) jsontext.Value {
	data, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		// What the callers give marshals: strings that are UTF-8, numbers and maps of those.
		return nil
	}
	return data
}

// jsonArray returns the array of the values, which are JSON, or nil if there are none.
func jsonArray(values []jsontext.Value) jsontext.Value {
	if len(values) == 0 {
		return nil
	}
	var b bytes.Buffer
	b.WriteByte('[')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(v)
	}
	b.WriteByte(']')
	return b.Bytes()
}

// stringValue makes the JSON string of s, if it is text that whiteroom can keep.
func stringValue(s string) (jsontext.Value, string) {
	switch {
	case strings.TrimSpace(s) == "":
		return nil, ""
	case len(s) > content.MaxTextBytes:
		return nil, fmt.Sprintf("it is too long: %d bytes (max %d)", len(s), content.MaxTextBytes)
	case !utf8.ValidString(s):
		return nil, "it is not UTF-8 text"
	}
	return jsonValue(s), ""
}

// numberValue makes the JSON number of s, a number written in decimal.
func numberValue(s string) (jsontext.Value, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ""
	}
	if !isDecimal(s) {
		return nil, "it is not a number"
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, "the number is out of range"
	}
	if f == 0 {
		return jsontext.Value("0"), ""
	}
	return jsontext.Value(strconv.FormatFloat(f, 'f', -1, 64)), ""
}

// isDecimal reports whether s is a number as JSON writes it, but with a sign, and with no digit before or after the point:
// 12, -3.5, +.5, 5. and 1e3.
func isDecimal(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digits := func() int {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		return i - start
	}
	n := digits()
	if i < len(s) && s[i] == '.' {
		i++
		n += digits()
	}
	if n == 0 {
		return false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if digits() == 0 {
			return false
		}
	}
	return i == len(s)
}

// boolValue makes true of 1, true, yes and on, in any case, and no value of 0, false, no and off.
func boolValue(s string) (jsontext.Value, string) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return nil, ""
	case "1", "true", "yes", "on":
		return jsontext.Value("true"), ""
	case "0", "false", "no", "off":
		return nil, ""
	}
	return nil, "it is not a boolean"
}

// dateValue makes the date of s, which is YYYY-MM-DD or YYYYMMDD, as the date picker of ACF writes it.
func dateValue(s string) (jsontext.Value, string) {
	s = strings.TrimSpace(s)
	if len(s) == len("20060102") && isDigits(s) {
		s = s[:4] + "-" + s[4:6] + "-" + s[6:]
	}
	if _, err := time.Parse(time.DateOnly, s); err != nil {
		return nil, "it is not a date"
	}
	return jsonValue(s), ""
}

// datetimePattern is the form of a date and time in RFC 3339 that whiteroom accepts, apart from the ranges.
var datetimePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`)

// datetimeValue makes the date and time of s: a time of the site as WordPress writes it, "2006-01-02 15:04:05", with the
// offset of the time zone of the site, or one in RFC 3339 that has an offset, as it is.
func (x *fieldConv) datetimeValue(s string) (jsontext.Value, string) {
	s = strings.TrimSpace(s)
	if t, ok := parsePostDate(s); ok {
		return jsonValue(wallTime(t, x.c.zone)), ""
	}
	if datetimePattern.MatchString(s) {
		if _, err := time.Parse(time.RFC3339, s); err == nil {
			return jsonValue(s), ""
		}
	}
	return nil, "it is not a date"
}

// fieldPostID reads s as the ID of a post in WordPress.
func fieldPostID(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	n, err := strconv.ParseInt(s, 10, 64)
	return n, isDigits(s) && err == nil && n >= 1
}

// post finds the entity of kind that the post n of WordPress became, or says why there is none. noun is what the post is
// called if it is not imported, and what is wanted is an attachment or an item otherwise, as a post of another kind is none.
func (x *fieldConv) post(n int64, kind content.RefKind, noun string) (id, why string) {
	id, ok := x.c.lookup(Object{Kind: ObjectPost, ID: n}, kind)
	if ok {
		return id, ""
	}
	if k, _, found := x.c.imp.IDs.Lookup(Object{Kind: ObjectPost, ID: n}); found && k != kind {
		return "", fmt.Sprintf("post %d is not an %s", n, noun)
	}
	return "", fmt.Sprintf("%s %d is not imported", noun, n)
}

func (x *fieldConv) media(f *content.FieldDef, s string) (jsontext.Value, string) {
	n, ok := fieldPostID(s)
	if !ok {
		return nil, "it is not the ID of an attachment"
	}
	id, why := x.post(n, content.RefMedia, "attachment")
	if why != "" {
		return nil, why
	}
	if ref, _ := x.c.imp.Ref(content.RefMedia, id); len(f.Accept) > 0 && !slices.Contains(f.Accept, ref.Type) {
		return nil, fmt.Sprintf("attachment %d is %s, which the field does not accept (accept: %s)", n, ref.Type, strings.Join(f.Accept, ", "))
	}
	return jsonValue(id), ""
}

func (x *fieldConv) entry(f *content.FieldDef, s string) (jsontext.Value, string) {
	n, ok := fieldPostID(s)
	if !ok {
		return nil, "it is not the ID of an item"
	}
	id, why := x.post(n, content.RefEntry, "item")
	if why != "" {
		return nil, why
	}
	if ref, _ := x.c.imp.Ref(content.RefEntry, id); len(f.Types) > 0 && !slices.Contains(f.Types, ref.Type) {
		return nil, fmt.Sprintf("item %d is of the type %s, which the field does not take (use %s)", n, typeText(ref.Type), strings.Join(f.Types, ", "))
	}
	return jsonValue(id), ""
}

func (x *fieldConv) term(f *content.FieldDef, s string) (jsontext.Value, string) {
	n, ok := fieldPostID(s)
	if !ok {
		return nil, "it is not the ID of a term"
	}
	taxonomy := f.Taxonomy
	if taxonomy == content.TaxonomyTag {
		taxonomy = "post_tag"
	}
	t := x.c.imp.WXR.Term(taxonomy, n)
	if t == nil {
		return nil, fmt.Sprintf("term %d is not in the WXR", n)
	}
	id, ok := x.c.lookup(TermObject(t), content.RefTerm)
	if !ok {
		return nil, fmt.Sprintf("term %d is not imported", n)
	}
	return jsonValue(id), ""
}

// link makes the link of v, which is a PHP list of the address, the title and the target, or the address. The title is not
// imported. If the address is one of the old site, it is a link to the entry or the medium that it is the address of, or a
// path of the site.
func (x *fieldConv) link(v phpserialize.Value) (jsontext.Value, string) {
	var href, target, title string
	if v.Kind == phpserialize.Array {
		if _, ok := v.Lookup("url"); !ok {
			return nil, `it is not a link: the list has no "url"`
		}
		for _, p := range []struct {
			key string
			to  *string
		}{{"url", &href}, {"target", &target}, {"title", &title}} {
			if e, ok := v.Lookup(p.key); ok {
				if s, ok := phpText(e); ok {
					*p.to = s
				}
			}
		}
	} else {
		s, ok := phpText(v)
		if !ok {
			return nil, "it is not a link"
		}
		href = s
	}
	href = content.NormalizeHref(href)
	if href == "" {
		return nil, ""
	}
	l, why := x.c.fieldLink(href)
	if l == nil {
		return nil, why
	}
	l.NewTab = strings.EqualFold(strings.TrimSpace(target), "_blank")
	if title = strings.TrimSpace(title); title != "" {
		x.note(Skipped, "the title %q of the link of the custom field %q is not imported", clip(title, maxQuoted), x.m.Key)
	}
	return jsonValue(l), ""
}

// fieldLink makes the link to href, an address that is not empty, as the links of a body are made (6.10.2): one to a page or
// a file of the old site that is imported goes to the entry or the medium, one to the site is a path of it, and another is the
// address as it is, if whiteroom accepts it. The reason is why there is no link.
func (c *entryConv) fieldLink(href string) (*content.Link, string) {
	if rest, ok := strings.CutPrefix(href, "#"); ok {
		if err := content.CheckName(rest); err != nil {
			return nil, fmt.Sprintf("the link to %q is not accepted: the anchor is not a name that whiteroom accepts", clip(href, maxQuoted))
		}
		return &content.Link{Anchor: rest}, ""
	}
	u, outside, ok := c.parseHref(href)
	if !ok {
		return c.externalLink(outside)
	}
	idx := c.imp.urlIndex()
	id, sitePath, same := idx.lookup(u)
	if !same {
		return c.externalLink(outside)
	}
	if id >= 1 {
		if kind, to, ok := c.imp.IDs.Lookup(Object{Kind: ObjectPost, ID: id}); ok {
			if _, ok := c.imp.Ref(kind, to); ok {
				switch kind {
				case content.RefEntry:
					l := &content.Link{Entry: to}
					if u.Fragment != "" && content.CheckName(u.Fragment) == nil {
						l.Anchor = u.Fragment
					}
					return l, ""
				case content.RefMedia:
					return &content.Link{Media: to}, ""
				case content.RefForm, content.RefTerm, content.RefAuthor, content.RefMenu, content.RefRedirect, content.RefRevision,
					content.RefTranslationGroup, content.RefUser:
				}
			}
		}
	}
	// A page of the old site that is not imported, or one that is not a post, such as the list of a term.
	h := sitePath
	if u.RawQuery != "" {
		h += "?" + u.RawQuery
	}
	if f := u.EscapedFragment(); f != "" {
		h += "#" + f
	}
	return c.externalLink(h)
}

// externalLink makes the link to the address href, if whiteroom accepts it.
func (c *entryConv) externalLink(href string) (*content.Link, string) {
	if err := content.CheckHref(href); err != nil {
		return nil, fmt.Sprintf("the link to %q is not accepted: %s", clip(href, maxQuoted), err)
	}
	return &content.Link{Href: href}, ""
}
