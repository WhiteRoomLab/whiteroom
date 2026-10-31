package content

import (
	"encoding/json/v2"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"testing"
)

// contactForm is testdata/contact-form.json: a contact form with a choice of topics and a consent to the privacy policy,
// which shows a message after sending in each language.
var contactForm = Form{
	ID:    testForm,
	Title: "お問い合わせ",
	Fields: []FormField{
		{Name: "your-name", Type: FormText, Label: Localized{"en": "Name", "ja": "お名前"}, Required: true, MaxLength: 100},
		{Name: "your-email", Type: FormEmail, Label: Localized{"en": "Email", "ja": "メールアドレス"}, Required: true},
		{Name: "your-tel", Type: FormTel, Label: Localized{"en": "Phone", "ja": "電話番号"}},
		{
			Name:     "topic",
			Type:     FormRadio,
			Label:    Localized{"en": "Topic", "ja": "ご用件"},
			Required: true,
			Options: []FormOption{
				{Value: "quote", Label: Localized{"en": "Quote", "ja": "お見積もり"}},
				{Value: "recruit", Label: Localized{"en": "Careers", "ja": "採用について"}},
				{Value: "other", Label: Localized{"en": "Other", "ja": "その他"}},
			},
		},
		{Name: "your-message", Type: FormTextarea, Label: Localized{"en": "Message", "ja": "お問い合わせ内容"}, Required: true, MaxLength: 2000},
		{
			Name:   "privacy",
			Type:   FormConsent,
			Label:  Localized{"en": "I agree to the handling of my personal information", "ja": "個人情報の取り扱いに同意する"},
			Policy: Localized{"en": testEnglishPrivacyPage, "ja": testPrivacyPage},
		},
	},
	SubmitLabel: Localized{"en": "Send", "ja": "送信"},
	Message: Localized{
		"en": "Thank you for contacting us.\nWe will reply within two business days.",
		"ja": "お問い合わせありがとうございます。\n2営業日以内にご返信します。",
	},
	Notify: []string{"info@example.com"},
}

// cloneForm returns a deep copy of x, whose fields, texts and addresses Normalize changes in place.
func cloneForm(x Form) Form {
	x.Fields = slices.Clone(x.Fields)
	for i := range x.Fields {
		f := &x.Fields[i]
		f.Label, f.Options, f.Policy = maps.Clone(f.Label), slices.Clone(f.Options), maps.Clone(f.Policy)
		for j := range f.Options {
			f.Options[j].Label = maps.Clone(f.Options[j].Label)
		}
	}
	x.SubmitLabel, x.Message, x.ThanksPage = maps.Clone(x.SubmitLabel), maps.Clone(x.Message), maps.Clone(x.ThanksPage)
	x.Notify = slices.Clone(x.Notify)
	return x
}

func TestFormUnmarshalJSON(t *testing.T) {
	in := readTestdata(t, "contact-form.json")
	var got Form
	if err := json.Unmarshal([]byte(in), &got); err != nil {
		t.Fatalf("Unmarshal(%s) = %v", in, err)
	}
	if !reflect.DeepEqual(got, contactForm) {
		t.Errorf("Unmarshal(%s) = %#v, want %#v", in, got, contactForm)
	}
}

// TestFormTestdata checks that testRefs agrees with testdata about the privacy policy pages, one in each language.
func TestFormTestdata(t *testing.T) {
	policy := contactForm.Fields[5].Policy
	for _, language := range testSite().Languages {
		id := policy[language]
		if want := (Ref{Type: TypePage, Language: language}); testRefs[id] != want {
			t.Errorf("testRefs[%q] = %#v, want %#v", id, testRefs[id], want)
		}
	}
}

func TestFormRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" if in is canonical
	}{
		{name: "contact form", in: readTestdata(t, "contact-form.json")},
		{name: "schemaVersion only", in: `{"schemaVersion":1}`},
		{
			name: "reversed key order",
			in: `{"notify":["sales@example.com","info@example.com"],"thanksPage":{"en":"` + testEnglishPage + `"},` +
				`"message":{"ja":"送信しました。"},"submitLabel":{"ja":"送信","en":"Send"},` +
				`"fields":[{"policy":{"ja":"` + testPrivacyPage + `","en":"` + testEnglishPrivacyPage + `"},` +
				`"options":[{"label":{"ja":"はい","en":"Yes"},"value":"yes"}],"maxLength":20,"minLength":2,"required":true,` +
				`"label":{"ja":"確認","en":"Check"},"type":"checkbox","name":"check"}],"title":"お問い合わせ","id":"` + testForm + `",` +
				`"schemaVersion":1}`,
			want: `{"schemaVersion":1,"id":"` + testForm + `","title":"お問い合わせ","fields":[{"name":"check","type":"checkbox",` +
				`"label":{"en":"Check","ja":"確認"},"required":true,"minLength":2,"maxLength":20,` +
				`"options":[{"value":"yes","label":{"en":"Yes","ja":"はい"}}],` +
				`"policy":{"en":"` + testEnglishPrivacyPage + `","ja":"` + testPrivacyPage + `"}}],` +
				`"submitLabel":{"en":"Send","ja":"送信"},"message":{"ja":"送信しました。"},"thanksPage":{"en":"` + testEnglishPage + `"},` +
				`"notify":["sales@example.com","info@example.com"]}`,
		},
		{
			name: "required false",
			in:   `{"schemaVersion":1,"fields":[{"name":"your-tel","type":"tel","required":false}]}`,
			want: `{"schemaVersion":1,"fields":[{"name":"your-tel","type":"tel"}]}`,
		},
		{
			// Validation reports them.
			name: "keys that the type does not use",
			in: `{"schemaVersion":1,"fields":[{"name":"a","type":"text","options":[{"value":"x"}],"policy":{"ja":"` + testPrivacyPage + `"}},` +
				`{"name":"b","type":"consent","required":true,"minLength":1},{"type":"color","maxLength":3}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			assertRoundTrip[Form](t, tt.in, want)
		})
	}
}

func TestFormMarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "empty", in: Form{}, want: `{"schemaVersion":1}`},
		{
			name: "empty fields, texts and addresses",
			in:   Form{Fields: []FormField{}, SubmitLabel: Localized{}, Message: Localized{}, ThanksPage: Localized{}, Notify: []string{}},
			want: `{"schemaVersion":1}`,
		},
		{name: "field with a name only", in: FormField{Name: "a"}, want: `{"name":"a"}`},
		{
			name: "field with empty texts and options",
			in:   FormField{Name: "a", Label: Localized{}, Options: []FormOption{}, Policy: Localized{}},
			want: `{"name":"a"}`,
		},
		{name: "option with an empty label", in: FormOption{Value: "a", Label: Localized{}}, want: `{"value":"a"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("Marshal(%#v) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormFieldUnmarshalJSON(t *testing.T) {
	topic := contactForm.Fields[3]
	in := `{"name":"topic","type":"radio","label":{"en":"Topic","ja":"ご用件"},"required":true,"options":[` +
		`{"value":"quote","label":{"en":"Quote","ja":"お見積もり"}},{"value":"recruit","label":{"en":"Careers","ja":"採用について"}},` +
		`{"value":"other","label":{"en":"Other","ja":"その他"}}]}`
	var field FormField
	if err := json.Unmarshal([]byte(in), &field); err != nil {
		t.Fatalf("Unmarshal(%s) = %v", in, err)
	}
	if !reflect.DeepEqual(field, topic) {
		t.Errorf("Unmarshal(%s) = %#v, want %#v", in, field, topic)
	}
	in = `{"value":"quote","label":{"en":"Quote","ja":"お見積もり"}}`
	var option FormOption
	if err := json.Unmarshal([]byte(in), &option); err != nil {
		t.Fatalf("Unmarshal(%s) = %v", in, err)
	}
	if !reflect.DeepEqual(option, topic.Options[0]) {
		t.Errorf("Unmarshal(%s) = %#v, want %#v", in, option, topic.Options[0])
	}
	// A field or an option alone is read as strictly as in a form.
	assertError(t, json.Unmarshal([]byte(`{"name":"a","Name":"b"}`), &field), "/Name", "unknown key")
	assertError(t, json.Unmarshal([]byte(`{"value":"a","label":{"ja":""}}`), &option), "/label/ja", "empty string is not allowed")
}

func TestFormUnmarshalJSONError(t *testing.T) {
	// field returns a form with the field f, a JSON object.
	field := func(f string) string { return `{"schemaVersion":1,"fields":[` + f + `]}` }
	tests := []struct {
		name string
		in   string
		path string
		msg  string
	}{
		{name: "empty", in: `{}`, path: "", msg: "empty object is not allowed"},
		{
			name: "missing schemaVersion",
			in: `{"id":"x","title":"x","fields":[{"name":"x","type":"x","label":{"ja":"x"}}],"submitLabel":{"ja":"x"},` +
				`"message":{"ja":"x"},"thanksPage":{"ja":"x"},"notify":["x"]}`,
			path: "",
			msg:  `missing key "schemaVersion"`,
		},
		{name: "schemaVersion 2", in: `{"schemaVersion":2}`, path: "/schemaVersion", msg: "unsupported schemaVersion 2 (use 1)"},
		{name: "language", in: `{"schemaVersion":1,"language":"ja"}`, path: "/language", msg: "unknown key"},
		{name: "spam protection", in: `{"schemaVersion":1,"honeypot":true}`, path: "/honeypot", msg: "unknown key"},
		{name: "key case", in: `{"schemaVersion":1,"ThanksPage":{"ja":"` + testAboutPage + `"}}`, path: "/ThanksPage", msg: "unknown key"},
		{name: "placeholder", in: field(`{"name":"x","placeholder":{"ja":"x"}}`), path: "/fields/0/placeholder", msg: "unknown key"},
		{name: "pattern", in: field(`{"name":"x","pattern":"[0-9]+"}`), path: "/fields/0/pattern", msg: "unknown key"},
		{name: "key case in a field", in: field(`{"Name":"x"}`), path: "/fields/0/Name", msg: "unknown key"},
		{name: "unknown key in a later field", in: field(`{"name":"a"},{"name":"b","multiple":true}`), path: "/fields/1/multiple", msg: "unknown key"},
		{
			name: "unknown key in an option",
			in:   field(`{"name":"x","options":[{"value":"a"},{"value":"b","selected":true}]}`),
			path: "/fields/0/options/1/selected",
			msg:  "unknown key",
		},
		{name: "empty fields", in: `{"schemaVersion":1,"fields":[]}`, path: "/fields", msg: "empty array is not allowed"},
		{name: "null fields", in: `{"schemaVersion":1,"fields":null}`, path: "/fields", msg: "expected array, got null"},
		{name: "field for fields", in: `{"schemaVersion":1,"fields":{"name":"x"}}`, path: "/fields", msg: "expected array, got object"},
		{name: "empty field", in: field(`{}`), path: "/fields/0", msg: "empty object is not allowed"},
		{name: "null field", in: field(`null`), path: "/fields/0", msg: "expected object, got null"},
		{name: "empty name", in: field(`{"name":""}`), path: "/fields/0/name", msg: "empty string is not allowed"},
		{name: "null type", in: field(`{"name":"x","type":null}`), path: "/fields/0/type", msg: "expected string, got null"},
		{name: "empty label", in: field(`{"name":"x","label":{}}`), path: "/fields/0/label", msg: "empty object is not allowed"},
		{name: "string for a label", in: field(`{"name":"x","label":"お名前"}`), path: "/fields/0/label", msg: "expected object, got string"},
		{
			name: "empty text in a label",
			in:   field(`{"name":"x","label":{"en":"Name","ja":""}}`),
			path: "/fields/0/label/ja",
			msg:  "empty string is not allowed",
		},
		{name: "required as a string", in: field(`{"name":"x","required":"true"}`), path: "/fields/0/required", msg: "expected boolean, got string"},
		{name: "null required", in: field(`{"name":"x","required":null}`), path: "/fields/0/required", msg: "expected boolean, got null"},
		{name: "minLength 0", in: field(`{"name":"x","minLength":0}`), path: "/fields/0/minLength", msg: "minLength 0 is out of range (1 or more)"},
		{name: "maxLength 0", in: field(`{"name":"x","maxLength":0}`), path: "/fields/0/maxLength", msg: "maxLength 0 is out of range (1 or more)"},
		{name: "negative maxLength", in: field(`{"name":"x","maxLength":-1}`), path: "/fields/0/maxLength", msg: "maxLength -1 is out of range (1 or more)"},
		{name: "minLength with a fraction", in: field(`{"name":"x","minLength":1.5}`), path: "/fields/0/minLength", msg: "must be an integer"},
		{name: "maxLength with an exponent", in: field(`{"name":"x","maxLength":1e2}`), path: "/fields/0/maxLength", msg: "must be an integer"},
		{name: "minLength as a string", in: field(`{"name":"x","minLength":"3"}`), path: "/fields/0/minLength", msg: "expected number, got string"},
		{name: "empty options", in: field(`{"name":"x","options":[]}`), path: "/fields/0/options", msg: "empty array is not allowed"},
		{name: "string for an option", in: field(`{"name":"x","options":["a"]}`), path: "/fields/0/options/0", msg: "expected object, got string"},
		{name: "empty option", in: field(`{"name":"x","options":[{}]}`), path: "/fields/0/options/0", msg: "empty object is not allowed"},
		{
			name: "empty value",
			in:   field(`{"name":"x","options":[{"value":"a"},{"value":""}]}`),
			path: "/fields/0/options/1/value",
			msg:  "empty string is not allowed",
		},
		{
			name: "number for a value",
			in:   field(`{"name":"x","options":[{"value":1}]}`),
			path: "/fields/0/options/0/value",
			msg:  "expected string, got number",
		},
		{
			name: "empty label of an option",
			in:   field(`{"name":"x","options":[{"value":"a","label":{}}]}`),
			path: "/fields/0/options/0/label",
			msg:  "empty object is not allowed",
		},
		{name: "empty policy", in: field(`{"name":"x","policy":{}}`), path: "/fields/0/policy", msg: "empty object is not allowed"},
		{
			name: "ID for the policy",
			in:   field(`{"name":"x","policy":"` + testPrivacyPage + `"}`),
			path: "/fields/0/policy",
			msg:  "expected object, got string",
		},
		{name: "empty submitLabel", in: `{"schemaVersion":1,"submitLabel":{}}`, path: "/submitLabel", msg: "empty object is not allowed"},
		{name: "string for the submitLabel", in: `{"schemaVersion":1,"submitLabel":"送信"}`, path: "/submitLabel", msg: "expected object, got string"},
		{name: "null submitLabel", in: `{"schemaVersion":1,"submitLabel":null}`, path: "/submitLabel", msg: "expected object, got null"},
		{
			name: "empty text in the submitLabel",
			in:   `{"schemaVersion":1,"submitLabel":{"en":"Send","ja":""}}`,
			path: "/submitLabel/ja",
			msg:  "empty string is not allowed",
		},
		{name: "empty message", in: `{"schemaVersion":1,"message":{}}`, path: "/message", msg: "empty object is not allowed"},
		{name: "string for the message", in: `{"schemaVersion":1,"message":"Thank you."}`, path: "/message", msg: "expected object, got string"},
		{name: "empty text in the message", in: `{"schemaVersion":1,"message":{"ja":""}}`, path: "/message/ja", msg: "empty string is not allowed"},
		{name: "empty thanksPage", in: `{"schemaVersion":1,"thanksPage":{}}`, path: "/thanksPage", msg: "empty object is not allowed"},
		{name: "null thanksPage", in: `{"schemaVersion":1,"thanksPage":null}`, path: "/thanksPage", msg: "expected object, got null"},
		{name: "empty notify", in: `{"schemaVersion":1,"notify":[]}`, path: "/notify", msg: "empty array is not allowed"},
		{name: "string for notify", in: `{"schemaVersion":1,"notify":"info@example.com"}`, path: "/notify", msg: "expected array, got string"},
		{name: "empty address", in: `{"schemaVersion":1,"notify":["info@example.com",""]}`, path: "/notify/1", msg: "empty string is not allowed"},
		{name: "null address", in: `{"schemaVersion":1,"notify":[null]}`, path: "/notify/0", msg: "expected string, got null"},
		{name: "duplicate key", in: `{"schemaVersion":1,"title":"a","title":"b"}`, path: "/title", msg: "duplicate key"},
		{name: "duplicate key in a field", in: field(`{"name":"a","name":"b"}`), path: "/fields/0/name", msg: "duplicate key"},
		{
			name: "duplicate key in an option",
			in:   field(`{"name":"x","options":[{"value":"a","value":"b"}]}`),
			path: "/fields/0/options/0/value",
			msg:  "duplicate key",
		},
		{
			name: "duplicate language in a label",
			in:   field(`{"name":"x","label":{"ja":"a","ja":"b"}}`),
			path: "/fields/0/label/ja",
			msg:  "duplicate key",
		},
		{
			name: "duplicate language in the submitLabel",
			in:   `{"schemaVersion":1,"submitLabel":{"ja":"送信","ja":"送る"}}`,
			path: "/submitLabel/ja",
			msg:  "duplicate key",
		},
		{name: "trailing data", in: `{"schemaVersion":1} {}`, path: "", msg: "unexpected data after value"},
	}
	// old makes a new form on each call, so that changes to its fields, texts or addresses in place show.
	old := func() Form {
		return Form{
			ID:          "old",
			Fields:      []FormField{{Name: "old", Label: Localized{"ja": "old"}, Options: []FormOption{{Value: "old"}}}},
			SubmitLabel: Localized{"ja": "old"},
			Message:     Localized{"ja": "old"},
			Notify:      []string{"old"},
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := old()
			assertError(t, got.UnmarshalJSON([]byte(tt.in)), tt.path, tt.msg)
			if !reflect.DeepEqual(got, old()) {
				t.Errorf("UnmarshalJSON(%s) changed the form to %#v", tt.in, got)
			}
		})
	}
}

func TestFormNormalize(t *testing.T) {
	const ctrl = string(rune(0x0001))
	tests := []struct {
		name string
		in   Form
		want Form
	}{
		{
			name: "control characters",
			in: Form{
				Title: "a" + ctrl + "b",
				Fields: []FormField{
					{Label: Localized{"ja": "c" + ctrl + "d"}},
					{Options: []FormOption{{Value: "e"}, {Value: "f" + ctrl + "g", Label: Localized{"en": "h" + ctrl + "i"}}}},
				},
				SubmitLabel: Localized{"ja": "j" + ctrl + "k"},
				Message:     Localized{"ja": "l" + ctrl + "m"},
			},
			want: Form{
				Title: "ab",
				Fields: []FormField{
					{Label: Localized{"ja": "cd"}},
					{Options: []FormOption{{Value: "e"}, {Value: "fg", Label: Localized{"en": "hi"}}}},
				},
				SubmitLabel: Localized{"ja": "jk"},
				Message:     Localized{"ja": "lm"},
			},
		},
		{
			name: "NFC",
			in: Form{
				Title:       "cafe" + acute,
				Fields:      []FormField{{Label: Localized{"en": "cafe" + acute}, Options: []FormOption{{Value: "cafe" + acute, Label: Localized{"en": "cafe" + acute}}}}},
				SubmitLabel: Localized{"en": "cafe" + acute},
				Message:     Localized{"en": "cafe" + acute},
			},
			want: Form{
				Title:       "caf" + eAcute,
				Fields:      []FormField{{Label: Localized{"en": "caf" + eAcute}, Options: []FormOption{{Value: "caf" + eAcute, Label: Localized{"en": "caf" + eAcute}}}}},
				SubmitLabel: Localized{"en": "caf" + eAcute},
				Message:     Localized{"en": "caf" + eAcute},
			},
		},
		{
			name: "line breaks and tabs left in the title, the labels and the values",
			in: Form{
				Title:       "a\r\nb\tc",
				Fields:      []FormField{{Label: Localized{"ja": "d\r\ne"}, Options: []FormOption{{Value: "f\r\ng", Label: Localized{"ja": "h\r\ni"}}}}},
				SubmitLabel: Localized{"ja": "j\r\nk\tl"},
			},
			want: Form{
				Title:       "a\r\nb\tc",
				Fields:      []FormField{{Label: Localized{"ja": "d\r\ne"}, Options: []FormOption{{Value: "f\r\ng", Label: Localized{"ja": "h\r\ni"}}}}},
				SubmitLabel: Localized{"ja": "j\r\nk\tl"},
			},
		},
		{
			name: "line breaks in the message",
			in:   Form{Message: Localized{"ja": "a\r\nb\rc\td", "en": "e\r\n"}},
			want: Form{Message: Localized{"ja": "a\nb\nc\td", "en": "e\n"}},
		},
		{
			name: "languages of which nothing is left",
			in: Form{
				Fields:      []FormField{{Label: Localized{"ja": "a", "en": ctrl}, Options: []FormOption{{Value: "b", Label: Localized{"ja": ctrl}}}}},
				SubmitLabel: Localized{"ja": "c", "en": ctrl},
				Message:     Localized{"ja": ctrl + "\v", "en": "d"},
			},
			want: Form{
				Fields:      []FormField{{Label: Localized{"ja": "a"}, Options: []FormOption{{Value: "b"}}}},
				SubmitLabel: Localized{"ja": "c"},
				Message:     Localized{"en": "d"},
			},
		},
		{
			// Validation reports them.
			name: "fields and options of which nothing is left",
			in:   Form{Fields: []FormField{{Label: Localized{"ja": ctrl}, Options: []FormOption{{Value: ctrl, Label: Localized{"ja": ctrl}}}}}},
			want: Form{Fields: []FormField{{Options: []FormOption{{}}}}},
		},
		{
			name: "spaces and control characters in the addresses",
			in:   Form{Notify: []string{" info@example.com\n", "sa" + ctrl + "les@example.com\t"}},
			want: Form{Notify: []string{"info@example.com", "sales@example.com"}},
		},
		{
			name: "domains in lower case",
			in:   Form{Notify: []string{"Info@Example.COM", " Sales@EXAMPLE.co.jp "}},
			want: Form{Notify: []string{"Info@example.com", "Sales@example.co.jp"}},
		},
		{
			// Validation reports the duplicate.
			name: "addresses that become the same",
			in:   Form{Notify: []string{"info@example.com", "info@Example.com "}},
			want: Form{Notify: []string{"info@example.com", "info@example.com"}},
		},
		{
			name: "addresses of which nothing is left",
			in:   Form{Notify: []string{" ", "info@example.com", ctrl + "\n"}},
			want: Form{Notify: []string{"info@example.com"}},
		},
		{name: "nothing left in the addresses", in: Form{Notify: []string{" ", "\t" + ctrl}}, want: Form{}},
		{name: "empty texts and addresses", in: Form{SubmitLabel: Localized{}, Message: Localized{}, Notify: []string{}}, want: Form{}},
		{
			name: "other items left as they are",
			in: Form{
				ID:         " x" + ctrl,
				Fields:     []FormField{{Name: "Your Name" + ctrl, Type: "TEXT ", Required: true, MinLength: -1, Policy: Localized{"ja": " " + testPrivacyPage + ctrl}}},
				ThanksPage: Localized{"ja": testAboutPage + "\n", "en": ctrl},
			},
			want: Form{
				ID:         " x" + ctrl,
				Fields:     []FormField{{Name: "Your Name" + ctrl, Type: "TEXT ", Required: true, MinLength: -1, Policy: Localized{"ja": " " + testPrivacyPage + ctrl}}},
				ThanksPage: Localized{"ja": testAboutPage + "\n", "en": ctrl},
			},
		},
		{name: "contact form", in: contactForm, want: contactForm},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Normalize changes the fields, the texts and the addresses in place.
			got := cloneForm(tt.in)
			got.Normalize()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Normalize() of %#v = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormValidate(t *testing.T) {
	const (
		ctrl     = string(rune(0x0001))
		missing  = "missing value; every language of the site needs one"
		reserved = `start with an ASCII letter (names starting with "_" are reserved)`
		types    = "(use text, email, tel, textarea, select, radio, checkbox, consent)"
	)
	// form returns x changed by f, which gets a deep copy of x.
	form := func(x Form, f func(x *Form)) Form {
		x = cloneForm(x)
		f(&x)
		return x
	}
	// fields returns the contact form with fs for its fields.
	fields := func(fs ...FormField) Form {
		return form(contactForm, func(x *Form) { x.Fields = fs })
	}
	// thanksPage returns the contact form with the message in ja only and id for the thanks page in en.
	thanksPage := func(id string) Form {
		return form(contactForm, func(x *Form) {
			delete(x.Message, "en")
			x.ThanksPage = Localized{"en": id}
		})
	}
	label := Localized{"en": "Item", "ja": "項目"}
	// textFields returns n text fields with different names.
	textFields := func(n int) []FormField {
		fs := make([]FormField, n)
		for i := range fs {
			fs[i] = FormField{Name: "item" + strconv.Itoa(i+1), Type: FormText, Label: label}
		}
		return fs
	}
	// choices returns n options with different values.
	choices := func(n int) []FormOption {
		opts := make([]FormOption, n)
		for i := range opts {
			v := strconv.Itoa(i + 1)
			opts[i] = FormOption{Value: v, Label: Localized{"en": v, "ja": v}}
		}
		return opts
	}
	// addresses returns n different email addresses.
	addresses := func(n int) []string {
		as := make([]string, n)
		for i := range as {
			as[i] = "info" + strconv.Itoa(i+1) + "@example.com"
		}
		return as
	}
	tests := []struct {
		name     string
		in       Form
		noLookup bool // validate without lookup
		paths    []string
		msg      string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "contact form", in: contactForm},
		{name: "contact form without lookup", in: contactForm, noLookup: true},
		{
			name: "required items only",
			in: Form{
				ID:          testForm,
				Title:       "お問い合わせ",
				Fields:      []FormField{{Name: "your-email", Type: FormEmail, Label: Localized{"en": "Email", "ja": "メールアドレス"}}},
				SubmitLabel: Localized{"en": "Send", "ja": "送信"},
				Message:     Localized{"en": "Thank you.", "ja": "ありがとうございます。"},
			},
		},
		{
			name:  "missing items",
			in:    Form{},
			paths: []string{"/id", "/title", "/fields", "/submitLabel", "/message"},
			msg: "/id: missing id\n/title: missing title\n/fields: missing fields\n/submitLabel: missing submitLabel\n" +
				"/message: missing message or thanksPage",
		},
		{
			// The errors are in document order.
			name: "invalid items",
			in: Form{
				ID:    testAboutPage,
				Title: "a\tb",
				Fields: []FormField{
					{Name: "_honeypot", Type: "hidden", Label: Localized{"ja": "c"}},
					{
						Name:      "privacy",
						Type:      FormConsent,
						Label:     Localized{"en": "d\ne", "ja": "f"},
						Required:  true,
						MinLength: 1,
						Options:   []FormOption{{Value: "yes"}},
						Policy:    Localized{"ja": testEnglishPrivacyPage},
					},
				},
				SubmitLabel: Localized{"ja": "g\nh"},
				Message:     Localized{"ja": "i" + ctrl},
				ThanksPage:  Localized{"ja": testAboutPage},
				Notify:      []string{"info@localhost"},
			},
			paths: []string{
				"/id", "/title", "/fields/0/name", "/fields/0/type", "/fields/0/label/en", "/fields/1/label/en",
				"/fields/1/required", "/fields/1/minLength", "/fields/1/options", "/fields/1/policy/ja",
				"/submitLabel/ja", "/submitLabel/en", "/message/ja", "/message/en", "/thanksPage/ja", "/notify/0",
			},
			msg: `/id: invalid form ID "` + testAboutPage + `": use "frm_" and a ULID (26 characters in upper case)` + "\n" +
				"/title: contains a line break or tab\n" +
				`/fields/0/name: invalid field name "_honeypot": ` + reserved + "\n" +
				`/fields/0/type: unknown field type "hidden" ` + types + "\n" +
				"/fields/0/label/en: " + missing + "\n" +
				"/fields/1/label/en: contains a line break or tab\n" +
				"/fields/1/required: not allowed for consent, which is always required\n" +
				"/fields/1/minLength: not allowed for consent fields\n" +
				"/fields/1/options: not allowed for consent fields\n" +
				`/fields/1/policy/ja: entry "` + testEnglishPrivacyPage + `" is in the language "en", not "ja"` + "\n" +
				"/submitLabel/ja: contains a line break or tab\n" +
				"/submitLabel/en: " + missing + "\n" +
				"/message/ja: contains control characters; normalize it first\n" +
				"/message/en: missing message or thanksPage in en\n" +
				"/thanksPage/ja: cannot be combined with message in ja\n" +
				`/notify/0: invalid email address "info@localhost": the domain needs a dot`,
		},
		{
			name:  "ID of another kind",
			in:    form(contactForm, func(x *Form) { x.ID = testAboutPage }),
			paths: []string{"/id"},
			msg:   `/id: invalid form ID "` + testAboutPage + `": use "frm_" and a ULID (26 characters in upper case)`,
		},
		{
			name:  "line break in the title",
			in:    form(contactForm, func(x *Form) { x.Title = "お問い合わせ\nフォーム" }),
			paths: []string{"/title"},
			msg:   "/title: contains a line break or tab",
		},
		{
			name:  "title not in NFC",
			in:    form(contactForm, func(x *Form) { x.Title = "cafe" + acute }),
			paths: []string{"/title"},
			msg:   "/title: not in Unicode NFC; normalize it first",
		},

		{name: "most fields", in: fields(textFields(MaxFormFields)...)},
		{
			name:  "too many fields",
			in:    fields(textFields(MaxFormFields + 1)...),
			paths: []string{"/fields"},
			msg:   "/fields: too many fields (101; max 100)",
		},
		{
			// The fields after the limit are checked too.
			name:  "invalid field after the limit",
			in:    fields(append(textFields(MaxFormFields), FormField{})...),
			paths: []string{"/fields", "/fields/100/name", "/fields/100/type", "/fields/100/label"},
			msg: "/fields: too many fields (101; max 100)\n" +
				"/fields/100/name: missing name\n/fields/100/type: missing type\n/fields/100/label: missing label",
		},
		{name: "name with digits, hyphens and underscores", in: form(contactForm, func(x *Form) { x.Fields[0].Name = "your_name-2" })},
		{name: "names in another case", in: form(contactForm, func(x *Form) { x.Fields[2].Name = "Your-Name" })},
		{
			name: "names starting with a, z, A and Z",
			in: fields(
				FormField{Name: "area", Type: FormText, Label: label},
				FormField{Name: "zip", Type: FormText, Label: label},
				FormField{Name: "Address", Type: FormText, Label: label},
				FormField{Name: "Zone", Type: FormText, Label: label},
			),
		},
		{
			name:  "missing name",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Name = "" }),
			paths: []string{"/fields/0/name"},
			msg:   "/fields/0/name: missing name",
		},
		{
			name:  "name starting with an underscore",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Name = "_name" }),
			paths: []string{"/fields/0/name"},
			msg:   `/fields/0/name: invalid field name "_name": ` + reserved,
		},
		{
			name:  "name starting with a digit",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Name = "1st-name" }),
			paths: []string{"/fields/0/name"},
			msg:   `/fields/0/name: invalid field name "1st-name": ` + reserved,
		},
		{
			name:  "name starting with a hyphen",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Name = "-name" }),
			paths: []string{"/fields/0/name"},
			msg:   `/fields/0/name: invalid field name "-name": ` + reserved,
		},
		{
			name:  "Japanese name",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Name = "お名前" }),
			paths: []string{"/fields/0/name"},
			msg:   `/fields/0/name: invalid name "お名前": use ASCII letters, digits, "-" and "_"`,
		},
		{
			name:  "name with a space",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Name = "your name" }),
			paths: []string{"/fields/0/name"},
			msg:   `/fields/0/name: invalid name "your name": use ASCII letters, digits, "-" and "_"`,
		},
		{
			name:  "duplicate name",
			in:    form(contactForm, func(x *Form) { x.Fields[2].Name = "your-name" }),
			paths: []string{"/fields/2/name"},
			msg:   `/fields/2/name: duplicate field name "your-name"`,
		},

		{
			name: "select and checkbox",
			in: fields(
				FormField{Name: "prefecture", Type: FormSelect, Label: label, Required: true, Options: choices(47)},
				FormField{Name: "services", Type: FormCheckbox, Label: label, Options: choices(3)},
			),
		},
		{
			// Without a type, which decides the keys that a field can use, the maxLength of the field is not checked.
			name:  "missing type",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Type = "" }),
			paths: []string{"/fields/0/type"},
			msg:   "/fields/0/type: missing type",
		},
		{
			name:  "unknown type",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Type = "color" }),
			paths: []string{"/fields/0/type"},
			msg:   `/fields/0/type: unknown field type "color" ` + types,
		},
		{
			name:  "type in upper case",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Type = "Text" }),
			paths: []string{"/fields/0/type"},
			msg:   `/fields/0/type: unknown field type "Text" ` + types,
		},
		{
			// Likewise, only the name, the type and the label are checked.
			name: "keys of an unknown type",
			in: fields(FormField{
				Name: "a", Type: "hidden", Label: label, Required: true, MinLength: -1, MaxLength: 3, Options: []FormOption{{}},
				Policy: Localized{"fr": ""},
			}),
			paths: []string{"/fields/0/type"},
			msg:   `/fields/0/type: unknown field type "hidden" ` + types,
		},

		{
			name:  "missing label",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Label = nil }),
			paths: []string{"/fields/0/label"},
			msg:   "/fields/0/label: missing label",
		},
		{
			name:  "label without en",
			in:    form(contactForm, func(x *Form) { delete(x.Fields[0].Label, "en") }),
			paths: []string{"/fields/0/label/en"},
			msg:   "/fields/0/label/en: " + missing,
		},
		{
			name:  "empty text in a label",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Label["ja"] = "" }),
			paths: []string{"/fields/0/label/ja"},
			msg:   "/fields/0/label/ja: " + missing,
		},
		{
			name:  "label in a language not in the site settings",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Label["fr"] = "Nom" }),
			paths: []string{"/fields/0/label/fr"},
			msg:   `/fields/0/label/fr: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "line break in a label",
			in:    form(contactForm, func(x *Form) { x.Fields[5].Label["ja"] = "個人情報の取り扱いに\n同意する" }),
			paths: []string{"/fields/5/label/ja"},
			msg:   "/fields/5/label/ja: contains a line break or tab",
		},
		{
			name:  "required consent",
			in:    form(contactForm, func(x *Form) { x.Fields[5].Required = true }),
			paths: []string{"/fields/5/required"},
			msg:   "/fields/5/required: not allowed for consent, which is always required",
		},

		{name: "minLength of a text field", in: form(contactForm, func(x *Form) { x.Fields[0].MinLength = 2 })},
		{name: "minLength of a textarea field", in: form(contactForm, func(x *Form) { x.Fields[4].MinLength = 10 })},
		{name: "minLength equal to maxLength", in: form(contactForm, func(x *Form) { x.Fields[0].MinLength = 100 })},
		{
			name:  "minLength greater than maxLength",
			in:    form(contactForm, func(x *Form) { x.Fields[0].MinLength = 101 }),
			paths: []string{"/fields/0/minLength"},
			msg:   "/fields/0/minLength: minLength 101 is greater than maxLength 100",
		},
		{
			name:  "negative maxLength",
			in:    form(contactForm, func(x *Form) { x.Fields[4].MaxLength = -1 }),
			paths: []string{"/fields/4/maxLength"},
			msg:   "/fields/4/maxLength: maxLength -1 is negative",
		},
		{
			name:  "minLength of an email field",
			in:    form(contactForm, func(x *Form) { x.Fields[1].MinLength = 6 }),
			paths: []string{"/fields/1/minLength"},
			msg:   "/fields/1/minLength: not allowed for email fields",
		},
		{
			name:  "maxLength of a tel field",
			in:    form(contactForm, func(x *Form) { x.Fields[2].MaxLength = 20 }),
			paths: []string{"/fields/2/maxLength"},
			msg:   "/fields/2/maxLength: not allowed for tel fields",
		},
		{
			name:  "minLength of a select field",
			in:    fields(FormField{Name: "a", Type: FormSelect, Label: label, MinLength: 1, Options: choices(2)}),
			paths: []string{"/fields/0/minLength"},
			msg:   "/fields/0/minLength: not allowed for select fields",
		},

		{name: "checkbox with one option", in: fields(FormField{Name: "newsletter", Type: FormCheckbox, Label: label, Options: choices(1)})},
		{
			name:  "missing options of a select field",
			in:    fields(FormField{Name: "a", Type: FormSelect, Label: label}),
			paths: []string{"/fields/0/options"},
			msg:   "/fields/0/options: missing options",
		},
		{
			name:  "missing options of a radio field",
			in:    form(contactForm, func(x *Form) { x.Fields[3].Options = nil }),
			paths: []string{"/fields/3/options"},
			msg:   "/fields/3/options: missing options",
		},
		{
			name:  "missing options of a checkbox field",
			in:    fields(FormField{Name: "a", Type: FormCheckbox, Label: label}),
			paths: []string{"/fields/0/options"},
			msg:   "/fields/0/options: missing options",
		},
		{
			name:  "options of a text field",
			in:    form(contactForm, func(x *Form) { x.Fields[0].Options = choices(2) }),
			paths: []string{"/fields/0/options"},
			msg:   "/fields/0/options: not allowed for text fields",
		},
		{name: "most options", in: form(contactForm, func(x *Form) { x.Fields[3].Options = choices(MaxFormOptions) })},
		{
			name:  "too many options",
			in:    form(contactForm, func(x *Form) { x.Fields[3].Options = choices(MaxFormOptions + 1) }),
			paths: []string{"/fields/3/options"},
			msg:   "/fields/3/options: too many options (1001; max 1000)",
		},
		{name: "Japanese value", in: form(contactForm, func(x *Form) { x.Fields[3].Options[0].Value = "お見積もり" })},
		{
			name:  "missing value",
			in:    form(contactForm, func(x *Form) { x.Fields[3].Options[1].Value = "" }),
			paths: []string{"/fields/3/options/1/value"},
			msg:   "/fields/3/options/1/value: missing value",
		},
		{
			name:  "duplicate value",
			in:    form(contactForm, func(x *Form) { x.Fields[3].Options[2].Value = "quote" }),
			paths: []string{"/fields/3/options/2/value"},
			msg:   `/fields/3/options/2/value: duplicate option "quote"`,
		},
		{
			name:  "line break in a value",
			in:    form(contactForm, func(x *Form) { x.Fields[3].Options[0].Value = "quote\n" }),
			paths: []string{"/fields/3/options/0/value"},
			msg:   "/fields/3/options/0/value: contains a line break or tab",
		},
		{
			name:  "line break in the label of an option",
			in:    form(contactForm, func(x *Form) { x.Fields[3].Options[0].Label["ja"] = "お見積\nもり" }),
			paths: []string{"/fields/3/options/0/label/ja"},
			msg:   "/fields/3/options/0/label/ja: contains a line break or tab",
		},
		{
			name:  "missing label of an option",
			in:    form(contactForm, func(x *Form) { x.Fields[3].Options[1].Label = nil }),
			paths: []string{"/fields/3/options/1/label"},
			msg:   "/fields/3/options/1/label: missing label",
		},
		{
			name:  "label of an option without en",
			in:    form(contactForm, func(x *Form) { delete(x.Fields[3].Options[1].Label, "en") }),
			paths: []string{"/fields/3/options/1/label/en"},
			msg:   "/fields/3/options/1/label/en: " + missing,
		},

		{name: "consent without a policy", in: form(contactForm, func(x *Form) { x.Fields[5].Policy = nil })},
		{name: "policy in one language", in: form(contactForm, func(x *Form) { delete(x.Fields[5].Policy, "en") })},
		{
			name:  "policies in the other languages",
			in:    form(contactForm, func(x *Form) { x.Fields[5].Policy = Localized{"en": testPrivacyPage, "ja": testEnglishPrivacyPage} }),
			paths: []string{"/fields/5/policy/ja", "/fields/5/policy/en"},
			msg: `/fields/5/policy/ja: entry "` + testEnglishPrivacyPage + `" is in the language "en", not "ja"` + "\n" +
				`/fields/5/policy/en: entry "` + testPrivacyPage + `" is in the language "ja", not "en"`,
		},
		{
			name:     "policies in the other languages without lookup",
			in:       form(contactForm, func(x *Form) { x.Fields[5].Policy = Localized{"en": testPrivacyPage, "ja": testEnglishPrivacyPage} }),
			noLookup: true,
		},
		{
			name:  "policy that does not exist",
			in:    form(contactForm, func(x *Form) { x.Fields[5].Policy["en"] = "ent_" + testMissing }),
			paths: []string{"/fields/5/policy/en"},
			msg:   `/fields/5/policy/en: entry "ent_` + testMissing + `" does not exist`,
		},
		{
			name:     "policy that does not exist without lookup",
			in:       form(contactForm, func(x *Form) { x.Fields[5].Policy["en"] = "ent_" + testMissing }),
			noLookup: true,
		},
		{
			name:  "policy of another kind",
			in:    form(contactForm, func(x *Form) { x.Fields[5].Policy["ja"] = testForm }),
			paths: []string{"/fields/5/policy/ja"},
			msg:   `/fields/5/policy/ja: invalid entry ID "` + testForm + `": use "ent_" and a ULID (26 characters in upper case)`,
		},
		{
			name:     "policy of another kind without lookup",
			in:       form(contactForm, func(x *Form) { x.Fields[5].Policy["ja"] = testForm }),
			noLookup: true,
			paths:    []string{"/fields/5/policy/ja"},
			msg:      `/fields/5/policy/ja: invalid entry ID "` + testForm + `"`,
		},
		{
			name:  "policy in a language not in the site settings",
			in:    form(contactForm, func(x *Form) { x.Fields[5].Policy["fr"] = testPrivacyPage }),
			paths: []string{"/fields/5/policy/fr"},
			msg:   `/fields/5/policy/fr: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "empty policy",
			in:    form(contactForm, func(x *Form) { x.Fields[5].Policy["en"] = "" }),
			paths: []string{"/fields/5/policy/en"},
			msg:   "/fields/5/policy/en: empty value; omit it",
		},
		{
			name:  "policy of an email field",
			in:    form(contactForm, func(x *Form) { x.Fields[1].Policy = Localized{"ja": testPrivacyPage} }),
			paths: []string{"/fields/1/policy"},
			msg:   "/fields/1/policy: not allowed for email fields",
		},

		{
			name:  "missing submitLabel",
			in:    form(contactForm, func(x *Form) { x.SubmitLabel = nil }),
			paths: []string{"/submitLabel"},
			msg:   "/submitLabel: missing submitLabel",
		},
		{
			name:  "submitLabel without en",
			in:    form(contactForm, func(x *Form) { delete(x.SubmitLabel, "en") }),
			paths: []string{"/submitLabel/en"},
			msg:   "/submitLabel/en: " + missing,
		},
		{
			name:  "empty text in the submitLabel",
			in:    form(contactForm, func(x *Form) { x.SubmitLabel["ja"] = "" }),
			paths: []string{"/submitLabel/ja"},
			msg:   "/submitLabel/ja: " + missing,
		},
		{
			name:  "submitLabel in a language not in the site settings",
			in:    form(contactForm, func(x *Form) { x.SubmitLabel["fr"] = "Envoyer" }),
			paths: []string{"/submitLabel/fr"},
			msg:   `/submitLabel/fr: language "fr" is not in the site settings (use ja, en)`,
		},
		{
			name:  "submitLabel not in NFC",
			in:    form(contactForm, func(x *Form) { x.SubmitLabel["en"] = "cafe" + acute }),
			paths: []string{"/submitLabel/en"},
			msg:   "/submitLabel/en: not in Unicode NFC; normalize it first",
		},
		{
			name:  "tab in the submitLabel",
			in:    form(contactForm, func(x *Form) { x.SubmitLabel["en"] = "Send\tnow" }),
			paths: []string{"/submitLabel/en"},
			msg:   "/submitLabel/en: contains a line break or tab",
		},

		{
			name: "thanks pages only",
			in: form(contactForm, func(x *Form) {
				x.Message, x.ThanksPage = nil, Localized{"en": testEnglishPage, "ja": testAboutPage}
			}),
		},
		{name: "message in ja and thanks page in en", in: thanksPage(testEnglishPage)},
		{
			name:  "message and thanks page in the same language",
			in:    form(contactForm, func(x *Form) { x.ThanksPage = Localized{"ja": testAboutPage} }),
			paths: []string{"/thanksPage/ja"},
			msg:   "/thanksPage/ja: cannot be combined with message in ja",
		},
		{
			name:  "missing message in en",
			in:    form(contactForm, func(x *Form) { delete(x.Message, "en") }),
			paths: []string{"/message/en"},
			msg:   "/message/en: missing message or thanksPage in en",
		},
		{
			name:  "missing message and thanks page",
			in:    form(contactForm, func(x *Form) { x.Message = nil }),
			paths: []string{"/message"},
			msg:   "/message: missing message or thanksPage",
		},
		{
			// The message in fr counts for no language of the site.
			name:  "message only in a language not in the site settings",
			in:    form(contactForm, func(x *Form) { x.Message = Localized{"fr": "Merci."} }),
			paths: []string{"/message/fr", "/message/ja", "/message/en"},
			msg: `/message/fr: language "fr" is not in the site settings (use ja, en)` + "\n" +
				"/message/ja: missing message or thanksPage in ja\n/message/en: missing message or thanksPage in en",
		},
		{
			name:  "thanks page in another language",
			in:    thanksPage(testAboutPage),
			paths: []string{"/thanksPage/en"},
			msg:   `/thanksPage/en: entry "` + testAboutPage + `" is in the language "ja", not "en"`,
		},
		{
			name:  "thanks page that does not exist",
			in:    thanksPage("ent_" + testMissing),
			paths: []string{"/thanksPage/en"},
			msg:   `/thanksPage/en: entry "ent_` + testMissing + `" does not exist`,
		},
		{name: "thanks page that does not exist without lookup", in: thanksPage("ent_" + testMissing), noLookup: true},
		{
			name:  "thanks page of another kind",
			in:    thanksPage(testForm),
			paths: []string{"/thanksPage/en"},
			msg:   `/thanksPage/en: invalid entry ID "` + testForm + `"`,
		},
		{
			// An empty value counts as no thanks page.
			name:  "empty thanks page",
			in:    thanksPage(""),
			paths: []string{"/message/en", "/thanksPage/en"},
			msg:   "/message/en: missing message or thanksPage in en\n/thanksPage/en: empty value; omit it",
		},
		{name: "tab in the message", in: form(contactForm, func(x *Form) { x.Message["en"] = "Thank you.\tWe will reply soon.\n" })},
		{
			name:  "message not in NFC",
			in:    form(contactForm, func(x *Form) { x.Message["en"] = "cafe" + acute }),
			paths: []string{"/message/en"},
			msg:   "/message/en: not in Unicode NFC; normalize it first",
		},
		{
			name:  "CR in the message",
			in:    form(contactForm, func(x *Form) { x.Message["ja"] = "a\r\nb" }),
			paths: []string{"/message/ja"},
			msg:   "/message/ja: contains control characters; normalize it first",
		},

		{name: "without notify", in: form(contactForm, func(x *Form) { x.Notify = nil })},
		{name: "most addresses", in: form(contactForm, func(x *Form) { x.Notify = addresses(MaxNotifyAddresses) })},
		{
			name:  "too many addresses",
			in:    form(contactForm, func(x *Form) { x.Notify = addresses(MaxNotifyAddresses + 1) }),
			paths: []string{"/notify"},
			msg:   "/notify: too many addresses (11; max 10)",
		},
		{
			name:  "address without a dot in the domain",
			in:    form(contactForm, func(x *Form) { x.Notify = []string{"info@localhost"} }),
			paths: []string{"/notify/0"},
			msg:   `/notify/0: invalid email address "info@localhost": the domain needs a dot`,
		},
		{
			name:  "upper case in the domain",
			in:    form(contactForm, func(x *Form) { x.Notify = []string{"info@example.com", "sales@Example.com"} }),
			paths: []string{"/notify/1"},
			msg:   `/notify/1: invalid email address "sales@Example.com": upper case in the domain; normalize it first`,
		},
		{
			name:  "duplicate address",
			in:    form(contactForm, func(x *Form) { x.Notify = []string{"info@example.com", "sales@example.com", "info@example.com"} }),
			paths: []string{"/notify/2"},
			msg:   `/notify/2: duplicate address "info@example.com"`,
		},
		{
			// The local part is case-sensitive.
			name: "addresses in another case",
			in:   form(contactForm, func(x *Form) { x.Notify = []string{"info@example.com", "Info@example.com"} }),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := testLookup
			if tt.noLookup {
				lookup = nil
			}
			err := tt.in.Validate(testSite(), lookup)
			if tt.paths == nil && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if got := errorPaths(t, err); !reflect.DeepEqual(got, tt.paths) {
				t.Errorf("Validate() paths = %q, want %q", got, tt.paths)
			}
			if tt.msg != "" {
				assertErrorMsg(t, err, tt.msg)
			}
		})
	}
}
