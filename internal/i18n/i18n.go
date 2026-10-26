// Package i18n holds all user-facing messages. Each language is a flat JSON file "key": "text"
// named by its locale code (locales/ru-RU.json, locales/en-US.json). Code and templates use only keys.
//
// Extra languages or overrides can be dropped into an external directory (GO24_LOCALES_DIR) without a rebuild:
// a file there adds a language or replaces individual keys of a built-in one. Missing keys fall back to the
// default language, then to the key itself.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
)

//go:embed locales/*.json
var embedded embed.FS

// Default is the language used when nothing else matches.
const Default = "ru-RU"

// Error is a translatable error: Key is a message id, Args fill its %s/%d placeholders.
// Args of type Ref are translated themselves (e.g. a status name inside an error).
type Error struct {
	Key  string
	Args []any
}

func (e *Error) Error() string {
	if len(e.Args) == 0 {
		return e.Key
	}
	return fmt.Sprintf("%s %v", e.Key, e.Args)
}

// E creates a translatable error.
func E(key string, args ...any) *Error { return &Error{Key: key, Args: args} }

// Ref is an argument that is itself a message key: E("err.action_not_allowed", Ref("action.start"), …).
type Ref string

type Lang struct {
	Code string
	Name string
}

type Bundle struct {
	msgs   map[string]map[string]string
	filter atomic.Pointer[Filter]
}

// Filter changes a message after it is found (the portal's date formats over the language's); nil — none.
type Filter func(key, msg string) string

// SetFilter installs (or, with nil, removes) the filter of messages.
func (b *Bundle) SetFilter(f Filter) {
	if f == nil {
		b.filter.Store(nil)
		return
	}
	b.filter.Store(&f)
}

// Load reads the built-in locales and, if dir is not empty, merges *.json files from it on top.
func Load(dir string) (*Bundle, error) {
	b := &Bundle{msgs: map[string]map[string]string{}}
	files, err := fs.Glob(embedded, "locales/*.json")
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		data, err := embedded.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if err := b.merge(f, data); err != nil {
			return nil, err
		}
	}
	if dir != "" {
		extra, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		for _, f := range extra {
			data, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			if err := b.merge(f, data); err != nil {
				return nil, err
			}
		}
	}
	if _, ok := b.msgs[Default]; !ok {
		return nil, fmt.Errorf("i18n: default locale %s is missing", Default)
	}
	return b, nil
}

func (b *Bundle) merge(path string, data []byte) error {
	code := strings.TrimSuffix(filepath.Base(path), ".json")
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("i18n: %s: %w", path, err)
	}
	if b.msgs[code] == nil {
		b.msgs[code] = map[string]string{}
	}
	for k, v := range m {
		b.msgs[code][k] = v
	}
	return nil
}

// Has reports whether the key is translated in lang or in the default language.
func (b *Bundle) Has(lang, key string) bool {
	if _, ok := b.msgs[lang][key]; ok {
		return true
	}
	_, ok := b.msgs[Default][key]
	return ok
}

// T translates key for lang. Args are passed to fmt.Sprintf; Ref and *Error args are translated first.
func (b *Bundle) T(lang, key string, args ...any) string {
	msg, ok := b.msgs[lang][key]
	if !ok {
		if msg, ok = b.msgs[Default][key]; !ok {
			msg = key
		}
	}
	if f := b.filter.Load(); f != nil {
		msg = (*f)(key, msg)
	}
	if len(args) == 0 {
		return msg
	}
	resolved := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case Ref:
			resolved[i] = b.T(lang, string(v))
		case *Error:
			resolved[i] = b.Err(lang, v)
		default:
			resolved[i] = a
		}
	}
	return fmt.Sprintf(msg, resolved...)
}

// Err translates an *Error; other errors are shown as a generic internal error.
func (b *Bundle) Err(lang string, err error) string {
	if e, ok := err.(*Error); ok {
		return b.T(lang, e.Key, e.Args...)
	}
	return b.T(lang, "err.internal")
}

// Langs lists available languages with their own names ("lang.name" key), sorted by code.
func (b *Bundle) Langs() []Lang {
	out := make([]Lang, 0, len(b.msgs))
	for code := range b.msgs {
		out = append(out, Lang{Code: code, Name: b.T(code, "lang.name")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

func (b *Bundle) Supported(code string) bool {
	_, ok := b.msgs[code]
	return ok
}

// Match picks the best language for an Accept-Language header: exact tag, then same primary language.
func (b *Bundle) Match(accept string) string {
	for _, part := range strings.Split(accept, ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if tag == "" || tag == "*" {
			continue
		}
		for code := range b.msgs {
			if strings.EqualFold(code, tag) {
				return code
			}
		}
		primary := strings.ToLower(strings.SplitN(tag, "-", 2)[0])
		var candidates []string
		for code := range b.msgs {
			if strings.ToLower(strings.SplitN(code, "-", 2)[0]) == primary {
				candidates = append(candidates, code)
			}
		}
		if len(candidates) > 0 {
			sort.Strings(candidates)
			return candidates[0]
		}
	}
	return Default
}
