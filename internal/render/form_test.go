package render

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const (
	contactForm  = "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6" // contact-form.json
	choicesForm  = "frm_01JB9F0A1B2C3D4E5F6G7H8J9K"
	lengthsForm  = "frm_01JB9F0A1B2C3D4E5F6G7H8J9M"
	consentsForm = "frm_01JB9F0A1B2C3D4E5F6G7H8J9N"
	textsForm    = "frm_01JB9F0A1B2C3D4E5F6G7H8J9P" // without some texts in ja
	typesForm    = "frm_01JB9F0A1B2C3D4E5F6G7H8J9Q" // with fields of unknown types
	escapingForm = "frm_01JB9F0A1B2C3D4E5F6G7H8J9R"
	emptyForm    = "frm_01JB9F0A1B2C3D4E5F6G7H8J9S" // without fields
	nilForm      = "frm_01JB9F0A1B2C3D4E5F6G7H8J9T" // found, but nil
	missingForm  = "frm_01JB9F0A1B2C3D4E5F6G7H8J9V"

	jaPrivacy = "ent_01JBD1A2B3C4D5E6F7G8H9J0K1" // at /privacy/
	enPrivacy = "ent_01JBD1M2N3P4Q5R6S7T8V9W0X1" // at /en/privacy/
)

// formTheme is the settings file of a theme whose forms have the variant boxed.
const formTheme = "schemaVersion: 1\nblocks:\n  form:\n    variants:\n      boxed: 枠付き\n"

func ja(s string) content.Localized { return content.Localized{"ja": s} }

// options returns the options of the values and the labels in ja that pairs alternate.
func options(pairs ...string) []content.FormOption {
	var o []content.FormOption
	for i := 0; i < len(pairs); i += 2 {
		o = append(o, content.FormOption{Value: pairs[i], Label: ja(pairs[i+1])})
	}
	return o
}

// testForms are forms in ja for the cases of TestForm, by ID.
var testForms = map[string]*content.Form{
	choicesForm: {
		ID: choicesForm,
		Fields: []content.FormField{
			{
				Name: "plan", Type: content.FormSelect, Label: ja("プラン"), Required: true,
				Options: options("basic", "ベーシック", "pro", "プロ"),
			},
			{Name: "size", Type: content.FormSelect, Label: ja("サイズ"), Options: options("m", "M")},
			{Name: "confirm", Type: content.FormCheckbox, Label: ja("確認"), Required: true, Options: options("yes", "確認しました")},
			{
				Name: "topics", Type: content.FormCheckbox, Label: ja("関心のある分野"), Required: true,
				Options: options("web", "Web", "print", "印刷", "video", "動画"),
			},
			{Name: "optional", Type: content.FormCheckbox, Label: ja("任意"), Options: options("a", "A")},
			{Name: "contact", Type: content.FormRadio, Label: ja("連絡方法"), Options: options("mail", "メール", "phone", "電話")},
		},
		SubmitLabel: ja("申し込む"),
	},
	lengthsForm: {
		ID: lengthsForm,
		Fields: []content.FormField{
			{Name: "company", Type: content.FormText, Label: ja("会社名"), MinLength: 2, MaxLength: 50},
			{Name: "detail", Type: content.FormTextarea, Label: ja("詳細"), Required: true, MinLength: 10},
			{Name: "code", Type: content.FormText, Label: ja("コード"), MinLength: 4},
			{Name: "note", Type: content.FormTextarea, Label: ja("備考")},
		},
		SubmitLabel: ja("送信"),
	},
	consentsForm: {
		ID: consentsForm,
		Fields: []content.FormField{
			{Name: "news", Type: content.FormConsent, Label: ja("お知らせを受け取る")},
			{Name: "terms", Type: content.FormConsent, Label: ja("規約に同意する"), Policy: content.Localized{"en": enPrivacy}},
			{Name: "draft", Type: content.FormConsent, Label: ja("下書きに同意する"), Policy: ja(draftEntry)},
			{Name: "missing", Type: content.FormConsent, Label: ja("無いものに同意する"), Policy: ja(missingEntry)},
			{Name: "privacy", Type: content.FormConsent, Label: ja("同意する"), Required: true, Policy: ja(jaPrivacy)},
			// Only consent fields have policies.
			{Name: "comment", Type: content.FormText, Label: ja("コメント"), Policy: ja(draftEntry)},
		},
		SubmitLabel: ja("送信"),
	},
	textsForm: {
		ID: textsForm,
		Fields: []content.FormField{
			{Name: "name", Type: content.FormText, Label: content.Localized{"en": "Name"}},
			{
				Name: "topic", Type: content.FormSelect, Label: ja("ご用件"),
				Options: []content.FormOption{{Value: "a", Label: ja("A")}, {Value: "b", Label: content.Localized{"en": "B"}}},
			},
			{Name: "agree", Type: content.FormConsent, Label: content.Localized{"ja": ""}},
		},
		SubmitLabel: content.Localized{"en": "Send"},
	},
	typesForm: {
		ID: typesForm,
		Fields: []content.FormField{
			{Name: "name", Type: content.FormText, Label: ja("お名前")},
			{Name: "birthday", Type: "date", Label: content.Localized{"en": "Birthday"}},
			{Name: "untyped", Label: ja("型なし")},
			{Name: "kind", Type: "Radio", Label: ja("種類"), Options: options("a", "A")},
			{Name: "mail", Type: content.FormEmail, Label: ja("メール"), Required: true},
		},
		SubmitLabel: ja("送信"),
	},
	escapingForm: {
		ID: escapingForm,
		Fields: []content.FormField{
			{Name: `a"b`, Type: content.FormText, Label: ja(`<b>"お名前"</b> & 'x'`)},
			{Name: "c'd", Type: content.FormRadio, Label: ja("<i>"), Options: options(`"v"`, "<o>", "a&b", "'p'")},
		},
		SubmitLabel: ja("送信 & 確認"),
	},
	emptyForm: {ID: emptyForm, SubmitLabel: ja("送信")},
	nilForm:   nil,
}

// formResolver returns a Resolver with the entries of linkResolver and the privacy pages jaPrivacy and enPrivacy, and
// the form of contact-form.json and testForms.
func formResolver(t *testing.T) testResolver {
	t.Helper()
	var contact content.Form
	decodeTestdata(t, "contact-form.json", contact.UnmarshalJSON)
	entries := maps.Clone(linkResolver.entries)
	entries[jaPrivacy] = [2]string{"プライバシーポリシー", "/privacy/"}
	entries[enPrivacy] = [2]string{"Privacy Policy", "/en/privacy/"}
	forms := maps.Clone(testForms)
	forms[contactForm] = &contact
	return testResolver{entries: entries, forms: forms}
}

// formJSON returns the JSON of a list of a form block with the ID b_0000form and the form id.
func formJSON(id string) string {
	return `[{"type": "form", "id": "b_0000form", "attrs": {"form": "` + id + `"}}]`
}

func TestForm(t *testing.T) {
	const at = "/0/attrs/form: "
	tests := []blockTest{
		{
			name: "contact form",
			json: formJSON(contactForm),
			want: `<form action="/_forms/frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6" method="post">
<input type="hidden" name="_language" value="ja">
<p><label>お名前<br><input type="text" name="your-name" required maxlength="100"></label></p>
<p><label>メールアドレス<br><input type="email" name="your-email" required></label></p>
<p><label>電話番号<br><input type="tel" name="your-tel"></label></p>
<fieldset>
<legend>ご用件</legend>
<label><input type="radio" name="topic" value="quote" required>お見積もり</label>
<label><input type="radio" name="topic" value="recruit" required>採用について</label>
<label><input type="radio" name="topic" value="other" required>その他</label>
</fieldset>
<p><label>お問い合わせ内容<br><textarea name="your-message" required maxlength="2000"></textarea></label></p>
<p><label><input type="checkbox" name="privacy" required>個人情報の取り扱いに同意する</label> <a href="/privacy/">プライバシーポリシー</a></p>
<p><button type="submit">送信</button></p>
</form>`,
		},
		{
			name: "select, checkbox and radio",
			json: formJSON(choicesForm),
			want: `<form action="/_forms/` + choicesForm + `" method="post">
<input type="hidden" name="_language" value="ja">
<p><label>プラン<br><select name="plan" required><option value="basic">ベーシック</option><option value="pro">プロ</option></select></label></p>
<p><label>サイズ<br><select name="size"><option value="m">M</option></select></label></p>
<fieldset>
<legend>確認</legend>
<label><input type="checkbox" name="confirm" value="yes" required>確認しました</label>
</fieldset>
<fieldset>
<legend>関心のある分野</legend>
<label><input type="checkbox" name="topics" value="web">Web</label>
<label><input type="checkbox" name="topics" value="print">印刷</label>
<label><input type="checkbox" name="topics" value="video">動画</label>
</fieldset>
<fieldset>
<legend>任意</legend>
<label><input type="checkbox" name="optional" value="a">A</label>
</fieldset>
<fieldset>
<legend>連絡方法</legend>
<label><input type="radio" name="contact" value="mail">メール</label>
<label><input type="radio" name="contact" value="phone">電話</label>
</fieldset>
<p><button type="submit">申し込む</button></p>
</form>`,
		},
		{
			name: "lengths",
			json: formJSON(lengthsForm),
			want: `<form action="/_forms/` + lengthsForm + `" method="post">
<input type="hidden" name="_language" value="ja">
<p><label>会社名<br><input type="text" name="company" minlength="2" maxlength="50"></label></p>
<p><label>詳細<br><textarea name="detail" required minlength="10"></textarea></label></p>
<p><label>コード<br><input type="text" name="code" minlength="4"></label></p>
<p><label>備考<br><textarea name="note"></textarea></label></p>
<p><button type="submit">送信</button></p>
</form>`,
		},
		{
			name: "consents and their policies",
			json: formJSON(consentsForm),
			want: `<form action="/_forms/` + consentsForm + `" method="post">
<input type="hidden" name="_language" value="ja">
<p><label><input type="checkbox" name="news" required>お知らせを受け取る</label></p>
<p><label><input type="checkbox" name="terms" required>規約に同意する</label></p>
<p><label><input type="checkbox" name="draft" required>下書きに同意する</label></p>
<p><label><input type="checkbox" name="missing" required>無いものに同意する</label></p>
<p><label><input type="checkbox" name="privacy" required>同意する</label> <a href="/privacy/">プライバシーポリシー</a></p>
<p><label>コメント<br><input type="text" name="comment"></label></p>
<p><button type="submit">送信</button></p>
</form>`,
			warnings: []string{
				at + `policy "` + draftEntry + `" of field "draft" is not published; rendered without the link`,
				at + `policy "` + missingEntry + `" of field "missing" does not exist; rendered without the link`,
			},
		},
		{
			name: "texts missing in the language",
			json: formJSON(textsForm),
			want: `<form action="/_forms/` + textsForm + `" method="post">
<input type="hidden" name="_language" value="ja">
<p><label><br><input type="text" name="name"></label></p>
<p><label>ご用件<br><select name="topic"><option value="a">A</option><option value="b"></option></select></label></p>
<p><label><input type="checkbox" name="agree" required></label></p>
<p><button type="submit"></button></p>
</form>`,
			warnings: []string{
				at + `form "` + textsForm + `" has no label of field "name" in ja; rendered empty`,
				at + `form "` + textsForm + `" has no label of option "b" of field "topic" in ja; rendered empty`,
				at + `form "` + textsForm + `" has no label of field "agree" in ja; rendered empty`,
				at + `form "` + textsForm + `" has no submitLabel in ja; rendered empty`,
			},
		},
		{
			name: "fields of unknown types",
			json: formJSON(typesForm),
			want: `<form action="/_forms/` + typesForm + `" method="post">
<input type="hidden" name="_language" value="ja">
<p><label>お名前<br><input type="text" name="name"></label></p>
<p><label>メール<br><input type="email" name="mail" required></label></p>
<p><button type="submit">送信</button></p>
</form>`,
			warnings: []string{
				at + `form "` + typesForm + `": field "birthday" has an unknown type "date"; left out`,
				at + `form "` + typesForm + `": field "untyped" has an unknown type ""; left out`,
				at + `form "` + typesForm + `": field "kind" has an unknown type "Radio"; left out`,
			},
		},
		{
			name: "escaping",
			json: formJSON(escapingForm),
			want: `<form action="/_forms/` + escapingForm + `" method="post">
<input type="hidden" name="_language" value="ja">
<p><label>&lt;b&gt;&#34;お名前&#34;&lt;/b&gt; &amp; &#39;x&#39;<br><input type="text" name="a&#34;b"></label></p>
<fieldset>
<legend>&lt;i&gt;</legend>
<label><input type="radio" name="c&#39;d" value="&#34;v&#34;">&lt;o&gt;</label>
<label><input type="radio" name="c&#39;d" value="a&amp;b">&#39;p&#39;</label>
</fieldset>
<p><button type="submit">送信 &amp; 確認</button></p>
</form>`,
		},
		{
			name: "no fields",
			json: formJSON(emptyForm),
			want: `<form action="/_forms/` + emptyForm + `" method="post">
<input type="hidden" name="_language" value="ja">
<p><button type="submit">送信</button></p>
</form>`,
		},
		{
			name: "classes and anchor",
			json: `[{
				"type": "form", "id": "b_0000form", "anchor": "contact", "variant": "boxed", "className": "wide",
				"style": {"padding": {"top": {"value": "1rem"}}}, "attrs": {"form": "` + emptyForm + `"}
			}]`,
			want: `<form id="contact" class="b-0000form is-variant-boxed wide" action="/_forms/` + emptyForm + `" method="post">
<input type="hidden" name="_language" value="ja">
<p><button type="submit">送信</button></p>
</form>`,
			css: ".b-0000form {\n  padding-top: 1rem !important;\n}\n",
		},
		{
			name: "missing form",
			json: `[
				` + paragraphJSON("b_0paragr1", "a") + `,
				{"type": "form", "id": "b_0000form", "variant": "x", "attrs": {"form": "` + missingForm + `"}},
				` + paragraphJSON("b_0paragr2", "b") + `
			]`,
			want:     "<p>a</p>\n<p>b</p>",
			warnings: []string{`/1/attrs/form: form "` + missingForm + `" does not exist; not rendered`},
		},
		{
			name:     "a nil form",
			json:     formJSON(nilForm),
			warnings: []string{at + `form "` + nilForm + `" does not exist; not rendered`},
		},
		{
			name:     "no form",
			json:     `[{"type": "form", "id": "b_0000form"}]`,
			warnings: []string{at + `form "" does not exist; not rendered`},
		},
		{
			name: "warnings in document order",
			json: `[{"type": "form", "id": "b_0000form", "variant": "big", "attrs": {"form": "` + textsForm + `"}}]`,
			want: `<form action="/_forms/` + textsForm + `" method="post">
<input type="hidden" name="_language" value="ja">
<p><label><br><input type="text" name="name"></label></p>
<p><label>ご用件<br><select name="topic"><option value="a">A</option><option value="b"></option></select></label></p>
<p><label><input type="checkbox" name="agree" required></label></p>
<p><button type="submit"></button></p>
</form>`,
			warnings: []string{
				`/0/variant: the theme has no form variant "big"; rendered without its class`,
				at + `form "` + textsForm + `" has no label of field "name" in ja; rendered empty`,
				at + `form "` + textsForm + `" has no label of option "b" of field "topic" in ja; rendered empty`,
				at + `form "` + textsForm + `" has no label of field "agree" in ja; rendered empty`,
				at + `form "` + textsForm + `" has no submitLabel in ja; rendered empty`,
			},
		},
	}
	checkBlocks(t, tests, formTheme, "ja", "", formResolver(t))
	checkBlocks(t, []blockTest{{
		name: "contact form in English with the base",
		json: formJSON(contactForm),
		want: `<form action="/blog/_forms/frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6" method="post">
<input type="hidden" name="_language" value="en">
<p><label>Name<br><input type="text" name="your-name" required maxlength="100"></label></p>
<p><label>Email<br><input type="email" name="your-email" required></label></p>
<p><label>Phone<br><input type="tel" name="your-tel"></label></p>
<fieldset>
<legend>Topic</legend>
<label><input type="radio" name="topic" value="quote" required>Quote</label>
<label><input type="radio" name="topic" value="recruit" required>Careers</label>
<label><input type="radio" name="topic" value="other" required>Other</label>
</fieldset>
<p><label>Message<br><textarea name="your-message" required maxlength="2000"></textarea></label></p>
<p><label><input type="checkbox" name="privacy" required>I agree to the handling of my personal information</label> ` +
			`<a href="/blog/en/privacy/">Privacy Policy</a></p>
<p><button type="submit">Send</button></p>
</form>`,
	}}, formTheme, "en", "/blog", formResolver(t))
}

// TestFormData checks the data of forms, also the fields that the default template does not write.
func TestFormData(t *testing.T) {
	res := formResolver(t)
	b := decodeBlocks(t, `[{"type": "form", "id": "b_0000form", "anchor": "contact", "attrs": {"form": "`+contactForm+`"}}]`)[0]
	w := &walker{r: newRenderer(t, testTheme(t, map[string]string{"theme.yaml": formTheme}), "en", "/blog", res)}
	got, ok := w.form(&b, "/0")
	want := Form{
		Common:   Common{ID: "b_0000form", Anchor: "contact"},
		Action:   "/blog/_forms/" + contactForm,
		Language: "en",
		Fields: []FormField{
			{Name: "your-name", Type: "text", Label: "Name", Required: true, MaxLength: 100},
			{Name: "your-email", Type: "email", Label: "Email", Required: true},
			{Name: "your-tel", Type: "tel", Label: "Phone"},
			{
				Name: "topic", Type: "radio", Label: "Topic", Required: true,
				Options: []FormOption{{"quote", "Quote"}, {"recruit", "Careers"}, {"other", "Other"}},
			},
			{Name: "your-message", Type: "textarea", Label: "Message", Required: true, MaxLength: 2000},
			{
				Name: "privacy", Type: "consent", Label: "I agree to the handling of my personal information", Required: true,
				Policy: &Policy{Href: "/blog/en/privacy/", Title: "Privacy Policy"},
			},
		},
		SubmitLabel: "Send",
	}
	if !ok || !reflect.DeepEqual(got, want) || w.warnings != nil {
		t.Errorf("form() =\n%+v, %v, warnings %q\nwant\n%+v", got, ok, w.warnings, want)
	}
	// Required as defined, also for a checkbox of several options, which the default template does not mark.
	b = decodeBlocks(t, formJSON(choicesForm))[0]
	w = &walker{r: newRenderer(t, testTheme(t, nil), "ja", "", res)}
	got, ok = w.form(&b, "/0")
	var required []string
	for _, f := range got.Fields {
		if f.Required {
			required = append(required, f.Name)
		}
	}
	if want := []string{"plan", "confirm", "topics"}; !ok || !slices.Equal(required, want) {
		t.Errorf("required fields = %q, %v, want %q", required, ok, want)
	}
	// Not rendered: the zero data.
	b = decodeBlocks(t, formJSON(missingForm))[0]
	w = &walker{r: newRenderer(t, testTheme(t, nil), "ja", "", res)}
	if got, ok := w.form(&b, "/0"); ok || !reflect.DeepEqual(got, Form{}) || len(w.warnings) != 1 {
		t.Errorf("form() of a missing form = %+v, %v, warnings %q, want nothing and a warning", got, ok, w.warnings)
	}
}
