// Command gen writes entities.go from entities.json of the HTML Standard, testdata/html5lib-tokenizer.json.gz from the
// tokenizer tests of html5lib-tests, and testdata/wpt-tree-construction.json.gz from the tree construction tests of
// web-platform-tests.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"go/format"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxSize = 16 << 20

const (
	entitiesURL    = "https://html.spec.whatwg.org/entities.json"
	entitiesSHA256 = "d741d877ac77c4194c4ad526b5b4a19aef8dfe411ab840a466891cdbb9f362e6"
)

// The tree construction tests moved from html5lib-tests to web-platform-tests in June 2026.
const (
	tokenizerURL    = "https://github.com/html5lib/html5lib-tests/archive/c777c408b61078ea2eb4acefc2535f54dbc8b28a.tar.gz"
	tokenizerSHA256 = "9537d1800b5b4058110891bc559b41a39d7e7a6802caef88d973faade788b35d"
	treeURL         = "https://raw.githubusercontent.com/web-platform-tests/wpt/4aa56d3beafc5655fa55491169eb14845b44eab3/html/syntax/parsing/resources/"
)

// treeFiles are all the .dat files in treeURL.
var treeFiles = []struct{ name, sha256 string }{
	{"adoption01.dat", "b2aba05bd1d832f73a0c6103b3c8b151b283bab7c56887274d81f3a062c4963e"},
	{"adoption02.dat", "e091e6976f861ae616fe56c527a78e7247ee7562bcafec996d4e4bda657bd9b7"},
	{"blocks.dat", "e3b7da1b57a4ec6991443dfc7ab3270f41d0f5959092a49be3e0edca0dae2104"},
	{"comments01.dat", "c2f3a5ab4baf24f360ee14272aad18c33deffb9a56c2d5964777b1b6e0b8e3c1"},
	{"doctype01.dat", "f3a286c09d729eeed9aa63e0aec13ab12336af04590c169543e6d1e1ef4723b2"},
	{"domjs-unsafe.dat", "eef4fb719e027ffaadfb854787d172893850fc561c29efe72d90c5bfa3c8b7ac"},
	{"entities01.dat", "b73605caac5aed5656184ab8db3f08edff5457ac3186cb04afa28384bca955e3"},
	{"entities02.dat", "f4e0cd461204b0184709be040c00811b776fbde1a6d002bb1c75d02056d9e54a"},
	{"foreign-fragment.dat", "73e1785753c66420c067e5b29f89b17e7ebe1079687a512040ef19c6d42ac2c2"},
	{"html5test-com.dat", "bc8b465fe4a3ef199142d7f7e8bc6bf7ba10327d3dd795d7abef993bcfb352d0"},
	{"inbody01.dat", "cb722f2853ec9613b71ea68e8bb26f474cc450b64612930591ab2656406222fe"},
	{"isindex.dat", "d152de773e276a07a1cfc91e93f01f3ce52c447c110192f2828dab51be2721b6"},
	{"main-element.dat", "d56e382994e1a5228ddb2ea49a5ff51ab68487f9a8f79ad34bcad05d40dc1e8f"},
	{"math.dat", "3c2ecc07272c175676ecfafa0cf6e18e74c3293075703702a4b7929fcb0d07bd"},
	{"menuitem-element.dat", "08e5e25f38bbc181c840ce5c5203b2566dc03129aeaf105ca2311645d40aae4d"},
	{"namespace-sensitivity.dat", "318fbc9926eddf5863524f1503c711ddaea93b55bcbd1eaab71b7e354ddfeb09"},
	{"noscript01.dat", "e82449304a6371c14ed490384b6d814b7b1eeb1022dbc5caa2045fc8400655d7"},
	{"pending-spec-changes-plain-text-unsafe.dat", "f45151f8dc7a4fe1a4b36710cf33606ff43cc98a42f1d2f085a77680683b0c99"},
	{"pending-spec-changes.dat", "a6b7c4ecccabe70de2f24184245e4a73a8d12ed44ee658c3d5d25cce04c9d27f"},
	{"plain-text-unsafe.dat", "016ec8acc054e4e8d7f5c156687b0ed563f271b3ac928dad9a9f1566f6c5f8f5"},
	{"processing-instructions.dat", "3d4c6e67b59fda8fa0eb798106a42d244c69a84d7e3a385f9845bfc40071b4f1"},
	{"quirks01.dat", "b6717cc15d4ed573ccf6755bc9d52073e675b4d09f23bde07961e27592b9717c"},
	{"ruby.dat", "5ae76ac4570d40e6648066798dd3ff729231bec98d3e010fdd67e0257f23a742"},
	{"scriptdata01.dat", "e32ea3adc3d68c90e62ea50b3a41bf80369d1491958532bf00787b3509667322"},
	{"scripted_adoption01.dat", "1203be63238a6effc9cc47e3c9b1fa9a0c7e0126a2f78916514afed2d46f42d8"},
	{"scripted_ark.dat", "93365261fd9ea7dd9242613b57c23dba94e66bfd837a5d928f725bed81012dd0"},
	{"scripted_foster01.dat", "4bc2c004bfd5efe3ca1b49f3b03de0b31d69347440c97ee1ed83b34ee16ad158"},
	{"scripted_webkit01.dat", "9d84f68ffd6e8b7b3924c9211280a70a888576b49074654cc9708236834afc58"},
	{"search-element.dat", "625ba3dcc1b8cf8688c0ee4ebd22308554a134b916f692bf64cbb54b8b705cae"},
	{"svg.dat", "4c819b8dbdfbd98cfbce9a535a304b0a16597eb29ccc04077131a62810f309cd"},
	{"tables01.dat", "bfd4a53246e3acc527c8bb214cc743082e19260e72365001aad3f1d8f4bd08dc"},
	{"template.dat", "72667fae7be5d2020b679409e45fed7b6923a3e9394da11a02c5bd9650323264"},
	{"tests1.dat", "aada6e3d12d624051bc978694475bd6a12bcedd7758781ba62425314684d23f3"},
	{"tests10.dat", "2d2624a819c323661e396d864ac23440053127b5ea7adb44a5904f5ceee5fa64"},
	{"tests11.dat", "276190e2a7b97e8fcf3bd863a4b4b5346b555a8336c00143cb1d0e8956b94a07"},
	{"tests12.dat", "e6c506cea74979a0d6ca47f6b175c7b6177d6e57db708920a89694e31dfd8a42"},
	{"tests14.dat", "d151b2426f38de40a5d4ae726e2a56dbc7742a9b1c95299d60c1e2e0fdad1f98"},
	{"tests15.dat", "ef784ece74cbd760da3a6947aaf4478810246b73c753f25a04be4d11dd806b2d"},
	{"tests16.dat", "3350be682713afc1f6dad37059f2551709497a3643c38c26e1fe36fd07d23745"},
	{"tests17.dat", "0567680775f58a5b2ad24e234f41d53f68fb2fc3ef7809bebaec0920e2f13c89"},
	{"tests18.dat", "5d0019ae43bb4e0b0da9f2e1d57ac0618a607bd8a1324b163ec7a23a1dc120f3"},
	{"tests19.dat", "cb2f0875d14da9d96f939b7d549953f8f26e70ea14861935d07d828ab04e0e1d"},
	{"tests2.dat", "9cf76b5f4890065c04fc82ae828379a55b85cbe76f584fb1ef23dcef0a77b86b"},
	{"tests20.dat", "07f7661690c4cd7cc0bbb0f1b9c1e1d65135e07c4dde8bbf106692f687e7d33d"},
	{"tests21.dat", "b1a67420c79a5131002fefc987084ffb6b6094a3a74b272c6f545a47df06452c"},
	{"tests22.dat", "78488328181d0f82f34b1a5e9e456ff7c713b3ff86dab6ace1530f6f07d5370d"},
	{"tests23.dat", "2e4752ff4ef898e4a0cf9a450e481440095163d45075a662846a062552afe148"},
	{"tests24.dat", "fdd5c21f60f42235ded224a03e7182d289088328f460525adaf3c14e772a24ac"},
	{"tests25.dat", "f2e08fda6d15a08faf9ff0001ec38560d942069cee6b4e264aeedbbacde7da8d"},
	{"tests26.dat", "d55d24dfca2444fba759d59346cbab21f7e70340dbb14e2c7af8da3d44abd4ab"},
	{"tests3.dat", "c4b4d8e0ea3d978c49d1e6a985d427164858b71ec980153c4526ed0c398f43b9"},
	{"tests4.dat", "792f9bb43b2b233a43b126d456550d17beb533ce0af4a4d24e34f09a68fd44e5"},
	{"tests5.dat", "bf80b927082290541781844906abdcb72f091488f7a11e8b7d9ac00076dd4fce"},
	{"tests6.dat", "be16c74d2a9862283262968439c95b9bfce6182ea61055a3c886c28d14f3e01a"},
	{"tests7.dat", "aeb9569589c809b1a563c0b163173e8cf980a6a4028cb7c210f19c181b56999a"},
	{"tests8.dat", "f3c8b1baece162e7e8c394540cdf9057266bb738bb0ad539a9d9b2f9d365f162"},
	{"tests9.dat", "857820f088506a6d20ea6acefbf19c0a6c4de87a39b24988878066397516b36f"},
	{"tests_innerHTML_1.dat", "acb9f835119e302d33204f437d54637a84ae26608ccb7bc961c6f7f44202a44c"},
	{"tricky01.dat", "3fb6d24c5e371860d096ef07f5fff38be3ceaa85f1239bee35cce548b596198a"},
	{"void-in-phrasing.dat", "c8855173aca8ecbd218abc26db34a631393ce0285fffebffdaf69b8bdd6224e9"},
	{"webkit01.dat", "063ca232535a792fa238ae769eeb0ddcc9bd2ee961d3da327ca134612d35d93c"},
	{"webkit02.dat", "03b215350d352faf110df2cc6eac23a44a7f70945b4ea962f0b17bed103459f7"},
}

type testFile struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

type entity struct {
	Codepoints []rune `json:"codepoints"`
	Characters string `json:"characters"`
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("gen: ")
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	client := &http.Client{Timeout: 5 * time.Minute}
	data, err := fetch(client, entitiesURL, entitiesSHA256)
	if err != nil {
		return err
	}
	src, n, err := entitiesSource(data)
	if err != nil {
		return fmt.Errorf("%s: %w", entitiesURL, err)
	}
	data, err = fetch(client, tokenizerURL, tokenizerSHA256)
	if err != nil {
		return err
	}
	tokenizer, err := tokenizerTests(data)
	if err != nil {
		return fmt.Errorf("%s: %w", tokenizerURL, err)
	}
	var tree []testFile
	for _, f := range treeFiles {
		data, err := fetch(client, treeURL+f.name, f.sha256)
		if err != nil {
			return err
		}
		tree = append(tree, testFile{f.name, string(data)})
	}
	if err := os.WriteFile("entities.go", src, 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		return err
	}
	if err := writeTests("html5lib-tokenizer.json.gz", tokenizer); err != nil {
		return err
	}
	if err := writeTests("wpt-tree-construction.json.gz", tree); err != nil {
		return err
	}
	log.Printf("%d entities, %d tokenizer test files, %d tree construction test files", n, len(tokenizer), len(tree))
	return nil
}

// fetch returns the body at url, which must have the sha256 sum.
func fetch(c *http.Client, url, sum string) ([]byte, error) {
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, maxSize)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != sum {
		return nil, fmt.Errorf("%s: sha256 is %s, want %s", url, got, sum)
	}
	return data, nil
}

// entitiesSource returns entities.go for data, the content of entities.json, and the number of entities.
func entitiesSource(data []byte) ([]byte, int, error) {
	var m map[string]entity
	if err := json.Unmarshal(data, &m, json.RejectUnknownMembers(true)); err != nil {
		return nil, 0, err
	}
	names := make([]string, 0, len(m))
	longest := 0
	for k, e := range m {
		name, ok := strings.CutPrefix(k, "&")
		if !ok || name == "" {
			return nil, 0, fmt.Errorf("name %q does not start with & or is empty", k)
		}
		valid := !slices.ContainsFunc(e.Codepoints, func(r rune) bool { return !utf8.ValidRune(r) })
		if !valid || e.Characters == "" || string(e.Codepoints) != e.Characters {
			return nil, 0, fmt.Errorf("%s: characters %+q are not the codepoints %v", k, e.Characters, e.Codepoints)
		}
		names = append(names, name)
		longest = max(longest, len(name))
	}
	slices.Sort(names)

	var b bytes.Buffer
	b.WriteString("// Code generated by \"go run ./gen\"; DO NOT EDIT.\n\n")
	b.WriteString("// Derived from entities.json of the HTML Standard. See WHATWG-LICENSE.txt.\n")
	fmt.Fprintf(&b, "// entities.json sha256:%s\n\npackage html\n\n", entitiesSHA256)
	fmt.Fprintf(&b, "const maxEntityLen = %d\n\nvar entities = map[string]string{\n", longest)
	for _, name := range names {
		fmt.Fprintf(&b, "\t%s: %s,\n", strconv.QuoteToASCII(name), strconv.QuoteToASCII(m["&"+name].Characters))
	}
	b.WriteString("}\n")
	src, err := format.Source(b.Bytes())
	return src, len(names), err
}

// tokenizerTests returns the files tokenizer/*.test in data, the archive of html5lib-tests.
func tokenizerTests(data []byte) ([]testFile, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var files []testFile
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		parts := strings.Split(h.Name, "/")
		if h.Typeflag != tar.TypeReg || len(parts) != 3 || parts[1] != "tokenizer" || !strings.HasSuffix(parts[2], ".test") {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", h.Name, err)
		}
		files = append(files, testFile{parts[2], string(b)})
	}
}

// writeTests writes files, sorted by name, to testdata/name as gzipped JSON.
func writeTests(name string, files []testFile) error {
	slices.SortFunc(files, func(a, b testFile) int { return strings.Compare(a.Name, b.Name) })
	js, err := json.Marshal(files)
	if err != nil {
		return err
	}
	// No name or time in the gzip header, so that the same files give the same bytes.
	var gz bytes.Buffer
	zw, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := zw.Write(append(js, '\n')); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join("testdata", name), gz.Bytes(), 0o644)
}
