package account

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const (
	maxLoginBytes = 64
	maxNameChars  = 256
)

// normalizeLogin removes the control characters and the surrounding spaces of s and maps the ASCII upper case letters
// to lower case. It does not use strings.ToLower, which maps U+212A KELVIN SIGN to "k": a login name that checkLogin
// refuses would turn into another one.
func normalizeLogin(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return -1
		case 'A' <= r && r <= 'Z':
			return r + 'a' - 'A'
		}
		return r
	}, s)
	return strings.TrimFunc(s, unicode.IsSpace)
}

func checkLogin(s string) error {
	if s == "" {
		return errors.New("missing login")
	}
	valid := len(s) <= maxLoginBytes && isLoginStart(s[0])
	for i := 0; valid && i < len(s); i++ {
		valid = isLoginStart(s[i]) || s[i] == '.' || s[i] == '-' || s[i] == '_'
	}
	if !valid {
		return fmt.Errorf(`invalid login name %q: use up to %d lower case ASCII letters, digits, ".", "-" and "_", starting with a letter or a digit`, s, maxLoginBytes)
	}
	return nil
}

func isLoginStart(c byte) bool {
	return 'a' <= c && c <= 'z' || '0' <= c && c <= '9'
}

func checkName(s string) error {
	if s == "" {
		return errors.New("missing name")
	}
	if err := content.CheckLine(s); err != nil {
		return err
	}
	if n := utf8.RuneCountInString(s); n > maxNameChars {
		return fmt.Errorf("too long: %d characters (max %d)", n, maxNameChars)
	}
	return nil
}

func checkEmail(s string) error {
	if s == "" {
		return errors.New("missing email")
	}
	return content.CheckEmail(s)
}

func checkRole(r Role) error {
	switch {
	case r == "":
		return errors.New("missing role")
	case !slices.Contains(Roles(), r):
		return fmt.Errorf("unknown role %q; use %s", r, listRoles())
	}
	return nil
}

// listRoles writes the roles as "admin, editor, author or viewer".
func listRoles() string {
	var names []string
	for _, r := range Roles() {
		names = append(names, string(r))
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// checkAuthor accepts "", which is no link, or the ID of an author profile; whether the profile exists is for the
// operations to check.
func checkAuthor(s string) error {
	if s == "" {
		return nil
	}
	return content.CheckID(content.RefAuthor, s)
}

// addProblem adds the problem err at path to errs, if there is one.
func addProblem(errs content.Errors, path string, err error) content.Errors {
	if err == nil {
		return errs
	}
	return append(errs, &content.Error{Path: path, Msg: err.Error()})
}

func (in *Input) normalize() {
	in.Login = normalizeLogin(in.Login)
	in.Name = content.NormalizeLine(in.Name)
	in.Email = content.NormalizeEmail(in.Email)
	in.Author = strings.TrimSpace(in.Author)
}

// check returns the problems of in, which normalize has normalized, in the order of its fields; nil if there are none.
func (in Input) check() content.Errors {
	var errs content.Errors
	errs = addProblem(errs, "/login", checkLogin(in.Login))
	errs = addProblem(errs, "/name", checkName(in.Name))
	errs = addProblem(errs, "/email", checkEmail(in.Email))
	errs = addProblem(errs, "/role", checkRole(in.Role))
	return addProblem(errs, "/author", checkAuthor(in.Author))
}

// normalize points the fields that c sets to their normalized values, and leaves the values that they pointed to.
func (c *Change) normalize() {
	if c.Login != nil {
		v := normalizeLogin(*c.Login)
		c.Login = &v
	}
	if c.Name != nil {
		v := content.NormalizeLine(*c.Name)
		c.Name = &v
	}
	if c.Email != nil {
		v := content.NormalizeEmail(*c.Email)
		c.Email = &v
	}
	if c.Author != nil {
		v := strings.TrimSpace(*c.Author)
		c.Author = &v
	}
}

// check returns the problems of the fields that c sets, which normalize has normalized, as Input.check does.
func (c Change) check() content.Errors {
	var errs content.Errors
	if c.Login != nil {
		errs = addProblem(errs, "/login", checkLogin(*c.Login))
	}
	if c.Name != nil {
		errs = addProblem(errs, "/name", checkName(*c.Name))
	}
	if c.Email != nil {
		errs = addProblem(errs, "/email", checkEmail(*c.Email))
	}
	if c.Role != nil {
		errs = addProblem(errs, "/role", checkRole(*c.Role))
	}
	if c.Author != nil {
		errs = addProblem(errs, "/author", checkAuthor(*c.Author))
	}
	return errs
}
