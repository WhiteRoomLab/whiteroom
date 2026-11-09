package account

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// mgrSecret is a text that nothing but the test may have, and the name by which a failure refers to it.
type mgrSecret struct{ name, text string }

// TestSecretsNotStored runs the operations that handle passwords and tokens in a data directory, and checks that only
// the hashes reach its files, and that no error and no printed account has a password, a token or a hash. A failure
// names the secret and never prints it.
func TestSecretsNotStored(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s, err := store.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := newMgrOn(t, s, dir)
	ctx := t.Context()

	var secrets []mgrSecret // the passwords in every form that the test used, and the tokens
	var errTexts []string   // what the operations returned as errors
	var prints []string     // the accounts as fmt prints them
	password := func(name string, forms ...string) {
		for _, f := range forms {
			secrets = append(secrets, mgrSecret{name, f})
		}
	}
	token := func(name, tok string) {
		secrets = append(secrets, mgrSecret{name, tok})
	}
	keep := func(err error) {
		if err != nil {
			errTexts = append(errTexts, err.Error())
		}
	}
	show := func(a Account) {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			prints = append(prints, fmt.Sprintf(verb, a), fmt.Sprintf(verb, &a), fmt.Sprintf(verb, []Account{a}))
		}
	}
	var sessions []mgrSecret // the tokens of the sessions, which are in the secrets too
	session := func(name, tok string) {
		token(name, tok)
		sessions = append(sessions, mgrSecret{name, tok})
	}
	showPrincipal := func(p Principal) {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			prints = append(prints, fmt.Sprintf(verb, p), fmt.Sprintf(verb, &p), fmt.Sprintf(verb, []Principal{p}),
				fmt.Sprintf(verb, p.Session), fmt.Sprintf(verb, &p.Session))
		}
	}

	const (
		adminFirst = "marker one first password"
		yamadaOne  = "marker two invited password"
		yamadaTwo  = "marker three changed password"
		taroOne    = "marker four first of taro"
		taroTwo    = "marker five after the reset"
		wrongOne   = "marker six wrong current one"
		wrongTwo   = "marker seven wrong password"
		tooShort   = "marker eight"
		cafeThree  = "marker ten changed again"
	)
	cafeOne, cafeTwo := "Caf"+eAcute+" marker nine password", "Cafe"+acute+" marker nine password"
	password("the first password", adminFirst)
	password("the password of yamada", yamadaOne)
	password("the new password of yamada", yamadaTwo)
	password("the password of taro", taroOne)
	password("the new password of taro", taroTwo)
	password("a wrong password", wrongOne)
	password("another wrong password", wrongTwo)
	password("a short password", tooShort)
	password("the password of cafe", cafeOne, cafeTwo)
	password("the new password of cafe", cafeThree)

	admin, err := e.m.CreateFirst(ctx, mgrInput("admin", RoleAdmin), adminFirst)
	keep(err)
	show(admin)

	yamada, tokenA, err := e.m.Invite(ctx, By(admin.ID), mgrInput("yamada", RoleEditor))
	keep(err)
	show(yamada)
	token("the invitation of yamada", tokenA)
	a, err := e.m.SetPassword(ctx, tokenA, yamadaOne)
	keep(err)
	show(a)
	_, err = e.m.SetPassword(ctx, tokenA, yamadaOne) // used up
	keep(err)
	keep(e.m.ChangePassword(ctx, yamada.ID, yamadaOne, yamadaTwo))
	keep(e.m.ChangePassword(ctx, yamada.ID, wrongOne, yamadaTwo))
	keep(e.m.ChangePassword(ctx, yamada.ID, yamadaTwo, tooShort))
	_, err = e.m.VerifyPasswordFactor(ctx, "yamada", wrongTwo)
	keep(err)
	_, err = e.m.VerifyPasswordFactor(ctx, "nobody", wrongTwo)
	keep(err)
	a, err = e.m.VerifyPasswordFactor(ctx, "yamada", yamadaTwo)
	keep(err)
	show(a)

	taro, tokenB, err := e.m.Invite(ctx, By(admin.ID), mgrInput("taro", RoleAuthor))
	keep(err)
	show(taro)
	token("the invitation of taro", tokenB)
	_, err = e.m.SetPassword(ctx, tokenB, tooShort)
	keep(err)
	a, err = e.m.SetPassword(ctx, tokenB, taroOne)
	keep(err)
	show(a)
	tokenC, err := e.m.Reset(ctx, By(admin.ID), taro.ID)
	keep(err)
	token("the reset of taro", tokenC)
	_, err = e.m.SetPassword(ctx, tokenB, taroTwo) // used up
	keep(err)
	a, err = e.m.SetPassword(ctx, tokenC, taroTwo)
	keep(err)
	show(a)

	cafe, tokenD, err := e.m.Invite(ctx, System, mgrInput("cafe", RoleViewer))
	keep(err)
	show(cafe)
	token("the invitation of cafe", tokenD)
	a, err = e.m.SetPassword(ctx, tokenD, cafeOne)
	keep(err)
	show(a)
	a, err = e.m.VerifyPasswordFactor(ctx, "cafe", cafeTwo)
	keep(err)
	show(a)
	keep(e.m.ChangePassword(ctx, cafe.ID, cafeTwo, cafeThree))

	// Two tokens stay pending: the rows hold their hashes.
	pending, tokenE, err := e.m.Invite(ctx, System, mgrInput("pending", RoleViewer))
	keep(err)
	show(pending)
	token("the invitation of pending", tokenE)
	tokenF, err := e.m.Reset(ctx, System, cafe.ID)
	keep(err)
	token("the reset of cafe", tokenF)
	list, err := e.m.List(ctx)
	keep(err)
	for _, acc := range list {
		show(acc)
	}

	// Two accounts log in, one of them twice, with its first session replaced by the second. The requests and the logout
	// are of the sessions that end up there, and of one that ended.
	adminSession, adminTok, err := e.m.Login(ctx, "admin", adminFirst, "")
	keep(err)
	showPrincipal(adminSession)
	session("the session of admin", adminTok)
	yamadaSession, yamadaTok1, err := e.m.Login(ctx, "yamada", yamadaTwo, "")
	keep(err)
	showPrincipal(yamadaSession)
	session("the first session of yamada", yamadaTok1)
	_, yamadaTok2, err := e.m.Login(ctx, "yamada", yamadaTwo, yamadaTok1)
	keep(err)
	session("the second session of yamada", yamadaTok2)
	_, _, err = e.m.Login(ctx, "yamada", wrongTwo, yamadaTok2) // fails, and keeps the session that it is given
	keep(err)
	got, err := e.m.Authenticate(ctx, yamadaTok2)
	keep(err)
	showPrincipal(got)
	_, err = e.m.Authenticate(ctx, yamadaTok1) // replaced
	keep(err)
	keep(e.m.Logout(ctx, adminTok))
	keep(e.m.Logout(ctx, yamadaTok1))
	_, err = e.m.Authenticate(ctx, adminTok) // logged out
	keep(err)

	// What the rows keep, which no error and no printed account may show either.
	var kept []mgrSecret
	rows := e.rawSessions(t)
	if len(rows) != 1 {
		t.Fatalf("the store holds %d session rows, want the second session of yamada only", len(rows))
	}
	for key, data := range rows {
		s, err := decodeSession(key, []byte(data))
		if err != nil {
			t.Fatal("a session row does not decode")
		}
		kept = append(kept, mgrSecret{"the key of a session row", key})
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			prints = append(prints, fmt.Sprintf(verb, s), fmt.Sprintf(verb, &s), fmt.Sprintf(verb, []sessionRow{s}))
		}
	}
	pendingTokens := make(map[string]string) // the hash of a pending token by the name of its token
	for i, r := range e.rows(t) {
		n := strconv.Itoa(i)
		if r.Password != "" {
			parts := strings.Split(r.Password, "$")
			kept = append(kept, mgrSecret{"the password hash of row " + n, r.Password},
				mgrSecret{"the salt of row " + n, parts[3]}, mgrSecret{"the key of row " + n, parts[4]})
		}
		if !r.Token.IsZero() {
			kept = append(kept, mgrSecret{"the token hash of row " + n, r.Token.Hash},
				mgrSecret{"the digits of the token hash of row " + n, strings.TrimPrefix(r.Token.Hash, "sha256:")})
			pendingTokens[r.Login] = r.Token.Hash
		}
	}
	if len(pendingTokens) != 2 || pendingTokens["pending"] != sumToken(tokenE) || pendingTokens["cafe"] != sumToken(tokenF) {
		t.Fatalf("the rows hold the token hashes of %d accounts, want those of pending and cafe, for their tokens", len(pendingTokens))
	}
	if len(errTexts) != 10 {
		t.Fatalf("the test kept %d error texts, want 10: those of the failed operations", len(errTexts))
	}
	for _, text := range append(append([]string{}, errTexts...), prints...) {
		for _, sec := range append(append([]mgrSecret{}, secrets...), kept...) {
			if strings.Contains(text, sec.text) {
				t.Errorf("an error or an account (%d bytes) contains %s", len(text), sec.name)
			}
		}
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	files := make(map[string][]byte)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		rel, _ := filepath.Rel(dir, path)
		files[rel] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for rel, data := range files {
		total += len(data)
		for _, sec := range secrets {
			if bytes.Contains(data, []byte(sec.text)) {
				t.Errorf("the file %s contains %s", rel, sec.name)
			}
		}
	}
	if len(files) == 0 || total == 0 {
		t.Fatalf("the data directory has %d files with %d bytes, want the files of the store", len(files), total)
	}
	// The rows are there: the hashes of the passwords, and the hashes of the tokens that are pending.
	has := func(want string) bool {
		for _, data := range files {
			if bytes.Contains(data, []byte(want)) {
				return true
			}
		}
		return false
	}
	if !has(`"password":"$pbkdf2-sha256$i=`) {
		t.Error("no file has a password hash")
	}
	for name, hash := range pendingTokens {
		if !has(`"hash":"` + hash + `"`) {
			t.Errorf("no file has the hash of the token of %s", name)
		}
	}
	// The key of a session is the SHA-256 of its token, also when the session has ended: the log keeps what it wrote.
	for _, sec := range sessions {
		if !has(sessKey(sec.text)) {
			t.Errorf("no file has the key of %s", sec.name)
		}
	}
}
