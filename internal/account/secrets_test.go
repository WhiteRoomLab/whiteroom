package account

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
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
	hashes := make(map[string]bool) // the password hashes that the rows have held, which no record may show
	snap := func() {
		for _, r := range e.rows(t) {
			if r.Password != "" {
				hashes[r.Password] = true
			}
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
		// yamadaThree is the password that yamada changes to through its session.
		yamadaThree = "marker eleven changed by itself"
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
	password("the password of yamada after the change through the session", yamadaThree)

	admin, err := e.m.CreateFirst(ctx, mgrInput("admin", RoleAdmin), adminFirst)
	keep(err)
	show(admin)
	snap()

	yamada, tokenA, err := e.m.Invite(ctx, By(admin.ID), mgrInput("yamada", RoleEditor))
	keep(err)
	show(yamada)
	token("the invitation of yamada", tokenA)
	a, err := e.m.SetPassword(ctx, mgrClient, tokenA, yamadaOne)
	keep(err)
	show(a)
	snap()
	_, err = e.m.SetPassword(ctx, mgrClient, tokenA, yamadaOne) // used up
	keep(err)
	keep(e.m.ChangePassword(ctx, yamada.ID, yamadaOne, yamadaTwo))
	snap()
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
	_, err = e.m.SetPassword(ctx, mgrClient, tokenB, tooShort)
	keep(err)
	a, err = e.m.SetPassword(ctx, mgrClient, tokenB, taroOne)
	keep(err)
	show(a)
	snap()
	tokenC, err := e.m.Reset(ctx, By(admin.ID), taro.ID)
	keep(err)
	token("the reset of taro", tokenC)
	_, err = e.m.SetPassword(ctx, mgrClient, tokenB, taroTwo) // used up
	keep(err)
	a, err = e.m.SetPassword(ctx, mgrClient, tokenC, taroTwo)
	keep(err)
	show(a)
	snap()

	cafe, tokenD, err := e.m.Invite(ctx, System, mgrInput("cafe", RoleViewer))
	keep(err)
	show(cafe)
	token("the invitation of cafe", tokenD)
	a, err = e.m.SetPassword(ctx, mgrClient, tokenD, cafeOne)
	keep(err)
	show(a)
	snap()
	a, err = e.m.VerifyPasswordFactor(ctx, "cafe", cafeTwo)
	keep(err)
	show(a)
	keep(e.m.ChangePassword(ctx, cafe.ID, cafeTwo, cafeThree))
	snap()

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
	adminSession, adminTok, err := e.m.Login(ctx, mgrClient, "admin", adminFirst, "")
	keep(err)
	showPrincipal(adminSession)
	session("the session of admin", adminTok)
	yamadaSession, yamadaTok1, err := e.m.Login(ctx, mgrClient, "yamada", yamadaTwo, "")
	keep(err)
	showPrincipal(yamadaSession)
	session("the first session of yamada", yamadaTok1)
	_, yamadaTok2, err := e.m.Login(ctx, mgrClient, "yamada", yamadaTwo, yamadaTok1)
	keep(err)
	session("the second session of yamada", yamadaTok2)
	_, _, err = e.m.Login(ctx, mgrClient, "yamada", wrongTwo, yamadaTok2) // fails, and keeps the session that it is given
	keep(err)
	got, err := e.m.Authenticate(ctx, mgrClient, yamadaTok2)
	keep(err)
	showPrincipal(got)
	_, err = e.m.Authenticate(ctx, mgrClient, yamadaTok1) // replaced
	keep(err)
	keep(e.m.Logout(ctx, mgrClient, adminTok))
	keep(e.m.Logout(ctx, mgrClient, yamadaTok1))
	_, err = e.m.Authenticate(ctx, mgrClient, adminTok) // logged out
	keep(err)

	// A password that is changed through its session: the session that asked ends, and a new one takes its place.
	_, _, err = e.m.ChangeOwnPassword(ctx, mgrClient, yamadaTok2, wrongOne, yamadaThree) // a wrong current password
	keep(err)
	_, _, err = e.m.ChangeOwnPassword(ctx, mgrClient, yamadaTok2, yamadaTwo, tooShort)
	keep(err)
	_, _, err = e.m.ChangeOwnPassword(ctx, mgrClient, tokSample, yamadaTwo, yamadaThree) // a token without a session
	keep(err)
	changed, yamadaTok3, err := e.m.ChangeOwnPassword(ctx, mgrClient, yamadaTok2, yamadaTwo, yamadaThree)
	keep(err)
	snap()
	showPrincipal(changed)
	session("the session of yamada after the password was changed", yamadaTok3)
	_, err = e.m.Authenticate(ctx, mgrClient, yamadaTok2) // ended by the change
	keep(err)

	// What the rows keep, which no error and no printed account may show either.
	var kept []mgrSecret
	rows := e.rawSessions(t)
	if len(rows) != 1 {
		t.Fatalf("the store holds %d session rows, want the session of yamada after the password was changed only", len(rows))
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
	if len(errTexts) != 14 {
		t.Fatalf("the test kept %d error texts, want 14: those of the failed operations", len(errTexts))
	}
	for _, text := range append(append([]string{}, errTexts...), prints...) {
		for _, sec := range append(append([]mgrSecret{}, secrets...), kept...) {
			if strings.Contains(text, sec.text) {
				t.Errorf("an error or an account (%d bytes) contains %s", len(text), sec.name)
			}
		}
	}

	// The records of the login history and the activity log show none of it either: not a password, not a token, not a hash
	// of one. The key of a session is the SHA-256 of its token, and is how a record names the session.
	forbidden := slices.Clone(secrets)
	for _, sec := range kept {
		if sec.name != "the key of a session row" {
			forbidden = append(forbidden, sec)
		}
	}
	for hash := range hashes {
		forbidden = append(forbidden, mgrSecret{"a password hash that a row has held", hash})
	}
	for name, tok := range map[string]string{
		"the invitation of yamada": tokenA, "the invitation of taro": tokenB, "the reset of taro": tokenC,
		"the invitation of cafe": tokenD, "the invitation of pending": tokenE, "the reset of cafe": tokenF,
	} {
		forbidden = append(forbidden, mgrSecret{"the hash of " + name, sumToken(tok)},
			mgrSecret{"the digits of the hash of " + name, strings.TrimPrefix(sumToken(tok), "sha256:")})
	}
	actions := make(map[audit.Action]bool)
	named := make(map[string]bool) // the sessions that a record names
	err = s.ReadAudit(ctx, audit.Filter{}, func(r audit.Record) bool {
		actions[r.Action] = true
		named[r.Session] = true
		data, err := audit.Encode(r)
		if err != nil {
			t.Errorf("a record of %s cannot be encoded", r.Action)
			return true
		}
		for _, sec := range forbidden {
			if bytes.Contains(data, []byte(sec.text)) {
				t.Errorf("a record of %s contains %s", r.Action, sec.name)
			}
		}
		return true
	})
	if err != nil {
		t.Fatalf("ReadAudit() = %v", err)
	}
	for _, action := range []audit.Action{audit.Login, audit.LoginFailure, audit.Logout, audit.SessionStart, audit.SessionEnd, audit.TokenFailure} {
		if !actions[action] {
			t.Errorf("no record is %s: the test does not reach the records that it checks", action)
		}
	}
	for _, sec := range sessions {
		if !named[sessKey(sec.text)] {
			t.Errorf("no record names %s by its key", sec.name)
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
