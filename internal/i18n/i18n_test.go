package i18n

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestLocalesComplete: every built-in locale has exactly the keys of the default one.
func TestLocalesComplete(t *testing.T) {
	b, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	def := b.msgs[Default]
	for code, msgs := range b.msgs {
		for k := range def {
			if _, ok := msgs[k]; !ok {
				t.Errorf("%s: missing key %s", code, k)
			}
		}
		for k := range msgs {
			if _, ok := def[k]; !ok {
				t.Errorf("%s: extra key %s (not in %s)", code, k, Default)
			}
		}
		if msgs["lang.name"] == "" {
			t.Errorf("%s: no lang.name", code)
		}
	}
}

func TestTranslate(t *testing.T) {
	b, _ := Load("")
	if got := b.T("en-US", "err.action_not_allowed", Ref("action.start"), Ref("status.completed")); got != "Action «Start» is not available in status «Completed»" {
		t.Fatalf("got %q", got)
	}
	if got := b.T("de-DE", "nav.projects"); got != "Проекты" {
		t.Fatalf("unknown language must fall back to %s, got %q", Default, got)
	}
	if got := b.T("en-US", "no.such.key"); got != "no.such.key" {
		t.Fatalf("missing key must return the key, got %q", got)
	}
	if got := b.Err("en-US", E("err.project_key_taken", "GO")); got != "Project with key GO already exists" {
		t.Fatalf("got %q", got)
	}
}

func TestMatch(t *testing.T) {
	b, _ := Load("")
	for in, want := range map[string]string{
		"en-GB,en;q=0.9,ru;q=0.8": "en-US",
		"ru":                      "ru-RU",
		"de-DE,de;q=0.9":          Default,
		"":                        Default,
		"EN-us":                   "en-US",
	} {
		if got := b.Match(in); got != want {
			t.Errorf("Match(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestExternalDir: a file in GO24_LOCALES_DIR adds a language or overrides single keys.
func TestExternalDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "de-DE.json"), []byte(`{"lang.name": "Deutsch", "nav.projects": "Projekte"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "ru-RU.json"), []byte(`{"nav.my_tasks": "Мои дела"}`), 0o644)
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, l := range b.Langs() {
		codes = append(codes, l.Code)
	}
	sort.Strings(codes)
	if len(codes) != 3 || b.T("de-DE", "nav.projects") != "Projekte" || b.T("de-DE", "nav.files") != "Файлы" {
		t.Fatalf("langs=%v de=%q", codes, b.T("de-DE", "nav.projects"))
	}
	if b.T("ru-RU", "nav.my_tasks") != "Мои дела" || b.T("ru-RU", "nav.projects") != "Проекты" {
		t.Fatal("override must replace only given keys")
	}
}
