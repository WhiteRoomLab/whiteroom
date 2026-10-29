package content

import (
	"encoding/json/v2"
	"maps"
	"reflect"
	"slices"
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
	x.Message, x.ThanksPage, x.Notify = maps.Clone(x.Message), maps.Clone(x.ThanksPage), slices.Clone(x.Notify)
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
				`"message":{"ja":"送信しました。"},"fields":[{"policy":{"ja":"` + testPrivacyPage + `","en":"` + testEnglishPrivacyPage + `"},` +
				`"options":[{"label":{"ja":"はい","en":"Yes"},"value":"yes"}],"maxLength":20,"minLength":2,"required":true,` +
				`"label":{"ja":"確認","en":"Check"},"type":"checkbox","name":"check"}],"title":"お問い合わせ","id":"` + testForm + `",` +
				`"schemaVersion":1}`,
			want: `{"schemaVersion":1,"id":"` + testForm + `","title":"お問い合わせ","fields":[{"name":"check","type":"checkbox",` +
				`"label":{"en":"Check","ja":"確認"},"required":true,"minLength":2,"maxLength":20,` +
				`"options":[{"value":"yes","label":{"en":"Yes","ja":"はい"}}],` +
				`"policy":{"en":"` + testEnglishPrivacyPage + `","ja":"` + testPrivacyPage + `"}}],` +
				`"message":{"ja":"送信しました。"},"thanksPage":{"en":"` + testEnglishPage + `"},` +
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
			in:   Form{Fields: []FormField{}, Message: Localized{}, ThanksPage: Localized{}, Notify: []string{}},
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
			in: `{"id":"x","title":"x","fields":[{"name":"x","type":"x","label":{"ja":"x"}}],"message":{"ja":"x"},` +
				`"thanksPage":{"ja":"x"},"notify":["x"]}`,
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
		{name: "trailing data", in: `{"schemaVersion":1} {}`, path: "", msg: "unexpected data after value"},
	}
	// old makes a new form on each call, so that changes to its fields, texts or addresses in place show.
	old := func() Form {
		return Form{
			ID:      "old",
			Fields:  []FormField{{Name: "old", Label: Localized{"ja": "old"}, Options: []FormOption{{Value: "old"}}}},
			Message: Localized{"ja": "old"},
			Notify:  []string{"old"},
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
				Message: Localized{"ja": "j" + ctrl + "k"},
			},
			want: Form{
				Title: "ab",
				Fields: []FormField{
					{Label: Localized{"ja": "cd"}},
					{Options: []FormOption{{Value: "e"}, {Value: "fg", Label: Localized{"en": "hi"}}}},
				},
				Message: Localized{"ja": "jk"},
			},
		},
		{
			name: "NFC",
			in: Form{
				Title:   "cafe" + acute,
				Fields:  []FormField{{Label: Localized{"en": "cafe" + acute}, Options: []FormOption{{Value: "cafe" + acute, Label: Localized{"en": "cafe" + acute}}}}},
				Message: Localized{"en": "cafe" + acute},
			},
			want: Form{
				Title:   "caf" + eAcute,
				Fields:  []FormField{{Label: Localized{"en": "caf" + eAcute}, Options: []FormOption{{Value: "caf" + eAcute, Label: Localized{"en": "caf" + eAcute}}}}},
				Message: Localized{"en": "caf" + eAcute},
			},
		},
		{
			name: "line breaks and tabs left in the title, the labels and the values",
			in:   Form{Title: "a\r\nb\tc", Fields: []FormField{{Label: Localized{"ja": "d\ne"}, Options: []FormOption{{Value: "f\tg", Label: Localized{"ja": "h\r\ni"}}}}}},
			want: Form{Title: "a\r\nb\tc", Fields: []FormField{{Label: Localized{"ja": "d\ne"}, Options: []FormOption{{Value: "f\tg", Label: Localized{"ja": "h\r\ni"}}}}}},
		},
		{
			name: "line breaks in the message",
			in:   Form{Message: Localized{"ja": "a\r\nb\rc\td", "en": "e\r\n"}},
			want: Form{Message: Localized{"ja": "a\nb\nc\td", "en": "e\n"}},
		},
		{
			name: "languages of which nothing is left",
			in: Form{
				Fields:  []FormField{{Label: Localized{"ja": "a", "en": ctrl}, Options: []FormOption{{Value: "b", Label: Localized{"ja": ctrl}}}}},
				Message: Localized{"ja": ctrl + "\v", "en": "c"},
			},
			want: Form{Fields: []FormField{{Label: Localized{"ja": "a"}, Options: []FormOption{{Value: "b"}}}}, Message: Localized{"en": "c"}},
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
		{name: "empty texts and addresses", in: Form{Message: Localized{}, Notify: []string{}}, want: Form{}},
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
