package wpimport

import (
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

func TestNew(t *testing.T) {
	e, rep := readSample(t, "wordpress-6.xml")
	imp := New("wordpress-6.xml", e, rep)
	if imp.File != "wordpress-6.xml" || imp.WXR != e || imp.Report != rep {
		t.Errorf("New = %+v, want the file, the export and the report as given", imp)
	}
	if imp.REST != nil || imp.RESTFile != "" {
		t.Errorf("New has REST %v and RESTFile %q, want none", imp.REST, imp.RESTFile)
	}
	if imp.IDs == nil {
		t.Fatal("New has no IDs")
	}
	// The IDs come from the address of the home page, as Seed has it: the ID of a post of the site that testSeed is.
	got, err := imp.IDs.Assign(Object{Kind: ObjectPost, ID: 123}, content.RefEntry)
	if want := "ent_0000000000QNCGZPVAWT8HNPBA"; err != nil || got != want {
		t.Errorf("Assign = %q, %v, want %s", got, err, want)
	}
	if want := NewTable(Seed(e)).NewID(content.RefRevision, "k"); imp.IDs.NewID(content.RefRevision, "k") != want {
		t.Error("the IDs of New do not come from Seed")
	}
	other := New("other.xml", e, NewReport(Info{}))
	if again, err := other.IDs.Assign(Object{Kind: ObjectPost, ID: 123}, content.RefEntry); err != nil || again != got {
		t.Errorf("Assign in another import of the site = %q, %v, want %s", again, err, got)
	}
}

func TestRefs(t *testing.T) {
	e, rep := readSample(t, "wordpress-6.xml")
	imp := New("wordpress-6.xml", e, rep)
	const id = "trm_0000000000AAFHG8EJHY38VMP6"
	ref := content.Ref{Type: "category", Language: "ja"}

	if r, ok := imp.Ref(content.RefTerm, id); ok || r != (content.Ref{}) {
		t.Errorf("Ref before AddRef = %+v, %v, want none", r, ok)
	}
	imp.AddRef(content.RefTerm, id, ref)
	if r, ok := imp.Ref(content.RefTerm, id); !ok || r != ref {
		t.Errorf("Ref = %+v, %v, want %+v", r, ok, ref)
	}
	// Only as the kind that it was recorded as.
	for _, kind := range allKinds {
		if kind == content.RefTerm {
			continue
		}
		if r, ok := imp.Ref(kind, id); ok || r != (content.Ref{}) {
			t.Errorf("Ref(%s) = %+v, %v, want none", kind, r, ok)
		}
	}
	if r, ok := imp.Ref(content.RefTerm, "trm_0000000000AAFHG8EJHY38VMP7"); ok || r != (content.Ref{}) {
		t.Errorf("Ref of another ID = %+v, %v, want none", r, ok)
	}

	// A record is replaced, also by one of another kind.
	replaced := content.Ref{Type: "tag", Language: "en"}
	imp.AddRef(content.RefTerm, id, replaced)
	if r, ok := imp.Ref(content.RefTerm, id); !ok || r != replaced {
		t.Errorf("Ref after a second AddRef = %+v, %v, want %+v", r, ok, replaced)
	}
	imp.AddRef(content.RefEntry, id, ref)
	if r, ok := imp.Ref(content.RefTerm, id); ok || r != (content.Ref{}) {
		t.Errorf("Ref as the old kind after AddRef as another = %+v, %v, want none", r, ok)
	}
	if r, ok := imp.Ref(content.RefEntry, id); !ok || r != ref {
		t.Errorf("Ref as the new kind = %+v, %v, want %+v", r, ok, ref)
	}

	// Another ID is not touched.
	const other = "ent_0000000000QNCGZPVAWT8HNPBA"
	imp.AddRef(content.RefEntry, other, content.Ref{Type: "page", Language: "ja"})
	imp.DropRef(id)
	if r, ok := imp.Ref(content.RefEntry, id); ok || r != (content.Ref{}) {
		t.Errorf("Ref after DropRef = %+v, %v, want none", r, ok)
	}
	if r, ok := imp.Ref(content.RefEntry, other); !ok || r.Type != "page" {
		t.Errorf("Ref of the other ID after DropRef = %+v, %v", r, ok)
	}
	imp.DropRef(id)
	imp.DropRef("ent_unknown")

	// It is the Lookup of the validation.
	lookup := content.ValidateOptions{Lookup: imp.Ref}.Lookup
	if r, ok := lookup(content.RefEntry, other); !ok || r.Language != "ja" {
		t.Errorf("Ref as a Lookup = %+v, %v", r, ok)
	}
}

func TestRefsOfAZeroImport(t *testing.T) {
	var imp Import
	imp.DropRef("trm_x")
	if _, ok := imp.Ref(content.RefTerm, "trm_x"); ok {
		t.Error("Ref of an Import with no records is true")
	}
	imp.AddRef(content.RefTerm, "trm_x", content.Ref{Type: "tag"})
	if r, ok := imp.Ref(content.RefTerm, "trm_x"); !ok || r.Type != "tag" {
		t.Errorf("Ref after AddRef on an Import that New did not make = %+v, %v", r, ok)
	}
}
