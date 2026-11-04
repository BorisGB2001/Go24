package legacyimport

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go24/internal/anon"
)

func TestMapStatus(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{status: 1, want: "pending"},
		{status: 2, want: "pending"},
		{status: 3, want: "in_progress"},
		{status: 4, want: "awaiting_control"},
		{status: 5, want: "completed"},
		{status: 6, want: "deferred"},
		{status: 7, want: "deferred"},
		{status: 0, want: "pending"},
		{status: -1, want: "pending"},
		{status: 8, want: "pending"},
		{status: 99, want: "pending"},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("status_%d", tc.status), func(t *testing.T) {
			if got := MapStatus(tc.status); got != tc.want {
				t.Errorf("MapStatus(%d) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

func TestClampPriority(t *testing.T) {
	tests := []struct {
		priority int
		want     int
	}{
		{priority: 0, want: 0},
		{priority: 1, want: 1},
		{priority: 2, want: 2},
		{priority: -1, want: 1},
		{priority: -100, want: 1},
		{priority: 3, want: 1},
		{priority: 100, want: 1},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("priority_%d", tc.priority), func(t *testing.T) {
			if got := clampPriority(tc.priority); got != tc.want {
				t.Errorf("clampPriority(%d) = %d, want %d", tc.priority, got, tc.want)
			}
		})
	}
}

func TestCleanText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "unescape html entities",
			in:   "&quot;Hello&quot; &amp; &lt;World&gt; &#39;test&#39;",
			want: "\"Hello\" & <World> 'test'",
		},
		{
			name: "crlf to lf conversion",
			in:   "line1\r\nline2\r\nline3",
			want: "line1\nline2\nline3",
		},
		{
			name: "whitespace trimming",
			in:   "  \t\r\n  hello world  \r\n\t  ",
			want: "hello world",
		},
		{
			name: "unicode cyrillic text",
			in:   "  &lt;Тест&gt; пройден успешно  ",
			want: "<Тест> пройден успешно",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanText(tc.in); got != tc.want {
				t.Errorf("cleanText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAtoi(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{in: "0", want: 0},
		{in: "42", want: 42},
		{in: "-7", want: -7},
		{in: "  123  ", want: 123},
		{in: "\t-456\n", want: -456},
		{in: "", want: 0},
		{in: "   ", want: 0},
		{in: "abc", want: 0},
		{in: "NULL", want: 0},
		{in: "12.34", want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := atoi(tc.in); got != tc.want {
				t.Errorf("atoi(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestB64(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "ascii string",
			in:   base64.StdEncoding.EncodeToString([]byte("hello world")),
			want: "hello world",
		},
		{
			name: "cyrillic utf8",
			in:   base64.StdEncoding.EncodeToString([]byte("Привет, мир!")),
			want: "Привет, мир!",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "invalid base64",
			in:   "not-base64!@#$%",
			want: "",
		},
		{
			name: "corrupted padding",
			in:   "====",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := b64(tc.in); got != tc.want {
				t.Errorf("b64(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "shorter than limit", in: "hello", n: 10, want: "hello"},
		{name: "exact limit", in: "hello", n: 5, want: "hello"},
		{name: "longer than limit ascii", in: "hello world", n: 5, want: "hello"},
		{name: "multi-byte runes cyrillic", in: "Привет, мир!", n: 6, want: "Привет"},
		{name: "multi-byte emojis", in: "😀😁😂😃😄", n: 3, want: "😀😁😂"},
		{name: "zero limit", in: "hello", n: 0, want: ""},
		{name: "empty string", in: "", n: 5, want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clip(tc.in, tc.n); got != tc.want {
				t.Errorf("clip(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestParseTime(t *testing.T) {
	msk := time.FixedZone("MSK", 3*3600)
	optsUTC := Options{Location: time.UTC}
	optsMSK := Options{Location: msk}

	tests := []struct {
		name    string
		opts    Options
		in      string
		wantNil bool
		want    time.Time
	}{
		{name: "empty string", opts: optsUTC, in: "", wantNil: true},
		{name: "NULL string", opts: optsUTC, in: "NULL", wantNil: true},
		{name: "zero date full", opts: optsUTC, in: "0000-00-00 00:00:00", wantNil: true},
		{name: "zero date prefix", opts: optsUTC, in: "0000-00-00", wantNil: true},
		{name: "invalid date format", opts: optsUTC, in: "2025/01/01 10:00:00", wantNil: true},
		{name: "date without time", opts: optsUTC, in: "2025-01-01", wantNil: true},
		{name: "invalid calendar date", opts: optsUTC, in: "2025-02-30 10:00:00", wantNil: true},
		{name: "valid datetime utc", opts: optsUTC, in: "2025-03-01 18:30:15", wantNil: false,
			want: time.Date(2025, 3, 1, 18, 30, 15, 0, time.UTC)},
		{name: "valid datetime msk", opts: optsMSK, in: "2025-03-01 18:30:15", wantNil: false,
			want: time.Date(2025, 3, 1, 18, 30, 15, 0, msk)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.opts.parseTime(tc.in)
			if tc.wantNil {
				if got != nil {
					t.Errorf("parseTime(%q) = %v, want nil", tc.in, *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("parseTime(%q) = nil, want %v", tc.in, tc.want)
			}
			if !got.Equal(tc.want) || got.Location() != tc.want.Location() {
				t.Errorf("parseTime(%q) = %v (%v), want %v (%v)", tc.in, *got, got.Location(), tc.want, tc.want.Location())
			}
		})
	}
}

func TestStatsString(t *testing.T) {
	st := Stats{
		Users:        10,
		Tasks:        25,
		Participants: 7,
		Parents:      3,
		Comments:     42,
	}
	want := "пользователей 10, задач 25, участников 7, связей родитель-подзадача 3, комментариев 42"
	if got := st.String(); got != want {
		t.Errorf("Stats.String() = %q, want %q", got, want)
	}

	zeroSt := Stats{}
	wantZero := "пользователей 0, задач 0, участников 0, связей родитель-подзадача 0, комментариев 0"
	if got := zeroSt.String(); got != wantZero {
		t.Errorf("Stats{}.String() = %q, want %q", got, wantZero)
	}
}

func TestReadTSV(t *testing.T) {
	dir := t.TempDir()

	t.Run("nonexistent file", func(t *testing.T) {
		_, err := readTSV(filepath.Join(dir, "no_such_file.tsv"), 3)
		if err == nil {
			t.Fatal("expected error for nonexistent file, got nil")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		p := filepath.Join(dir, "empty.tsv")
		if err := os.WriteFile(p, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		rows, err := readTSV(p, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("expected 0 rows, got %d", len(rows))
		}
	})

	t.Run("valid rows with empty lines", func(t *testing.T) {
		p := filepath.Join(dir, "valid.tsv")
		content := "a\tb\tc\n\nd\te\tf\n\n"
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		rows, err := readTSV(p, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("expected 2 rows, got %d", len(rows))
		}
		if rows[0][0] != "a" || rows[0][1] != "b" || rows[0][2] != "c" {
			t.Errorf("row 0 mismatch: %v", rows[0])
		}
		if rows[1][0] != "d" || rows[1][1] != "e" || rows[1][2] != "f" {
			t.Errorf("row 1 mismatch: %v", rows[1])
		}
	})

	t.Run("column count mismatch", func(t *testing.T) {
		p := filepath.Join(dir, "mismatch.tsv")
		// Line 1: 3 columns; Line 2: 2 columns; want 3
		content := "a\tb\tc\nx\ty\n"
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := readTSV(p, 3)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		wantMsg := "mismatch.tsv:2: 2 columns, want 3"
		if err.Error() != wantMsg {
			t.Errorf("error = %q, want %q", err.Error(), wantMsg)
		}
	})

	t.Run("empty fields between tabs", func(t *testing.T) {
		p := filepath.Join(dir, "empty_fields.tsv")
		content := "1\t\t3\n"
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		rows, err := readTSV(p, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rows) != 1 || len(rows[0]) != 3 {
			t.Fatalf("unexpected rows: %v", rows)
		}
		if rows[0][0] != "1" || rows[0][1] != "" || rows[0][2] != "3" {
			t.Errorf("row content mismatch: %v", rows[0])
		}
	})
}

func TestCleanBBCode(t *testing.T) {
	a := anon.New("test-clean-bbcode-seed", "portal.import")
	p17 := a.AddPerson(anon.Person{ID: 17, First: "Сергей", Last: "Кузнецов", Middle: "Петрович", Gender: "M"})
	p25 := a.AddPerson(anon.Person{ID: 25, First: "Ольга", Last: "Смирнова", Gender: "F"})

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "basic formatting tags",
			in:   "[B]bold[/B] [I]italic[/I] [U]underline[/U] [S]strike[/S]",
			want: "bold italic underline strike",
		},
		{
			name: "quotes and code",
			in:   "[QUOTE]quote text[/QUOTE] and [CODE]const x = 1[/CODE]",
			want: "quote text and const x = 1",
		},
		{
			name: "tags with attributes",
			in:   "[COLOR=#ff0000]red[/COLOR] [SIZE=14pt]large[/SIZE] [FONT=Arial]font[/FONT]",
			want: "red large font",
		},
		{
			name: "alignment and paragraphs",
			in:   "[LEFT]left[/LEFT] [RIGHT]right[/RIGHT] [CENTER]center[/CENTER] [JUSTIFY]justified[/JUSTIFY] [P]paragraph[/P]",
			want: "left right center justified paragraph",
		},
		{
			name: "spoiler and hr",
			in:   "[SPOILER]secret[/SPOILER][HR]",
			want: "secret",
		},
		{
			name: "tables",
			in:   "[TABLE][TR][TH]Header[/TH][TD]Cell[/TD][/TR][/TABLE]",
			want: "HeaderCell",
		},
		{
			name: "list items",
			in:   "[LIST][*]Item 1[*]Item 2[/LIST]",
			want: "• Item 1• Item 2",
		},
		{
			name: "disk file attachments",
			in:   "File attached: [DISK FILE ID=n12345] and [disk file id=678]",
			want: "File attached: [вложение] and [вложение]",
		},
		{
			name: "url conversion",
			in:   "Visit [URL=https://example.com/tasks?a=1]our task[/URL] now",
			want: "Visit our task (https://example.com/tasks?a=1) now",
		},
		{
			name: "url case insensitive",
			in:   "[url=http://go24.local]Go24[/url]",
			want: "Go24 (http://go24.local)",
		},
		{
			name: "known user mention",
			in:   "Задачу ведёт [USER=17]Сергей Кузнецов[/USER]",
			want: "Задачу ведёт @" + p17.FullName(),
		},
		{
			name: "another known user mention",
			in:   "Согласовано с [USER=25]Ольга Смирнова[/USER]",
			want: "Согласовано с @" + p25.FullName(),
		},
		{
			name: "unknown user mention",
			in:   "Обратитесь к [USER=999]Иван Иванович[/USER]",
			want: "Обратитесь к @пользователь",
		},
		{
			name: "user mention with zero id",
			in:   "Автор: [USER=0]Гость[/USER]",
			want: "Автор: @пользователь",
		},
		{
			name: "nested tags",
			in:   "[B][I]bold and italic[/I][/B]",
			want: "bold and italic",
		},
		{
			name: "html entities and crlf in bbcode",
			in:   "[B]&quot;Цитата&quot; &amp; текст[/B]\r\nВторая строка",
			want: "\"Цитата\" & текст\nВторая строка",
		},
		{
			name: "complex combined legacy task description",
			in: "[B]Срочно![/B] Позвонить клиенту. [URL=https://crm.corp.ru/deal/10]Сделка #10[/URL].\r\n" +
				"Ответственный: [USER=17]Кузнецов С.[/USER]. [DISK FILE ID=n55]\r\n" +
				"[LIST][*]Уточнить ТЗ[*]Выставить счёт[/LIST]",
			want: "Срочно! Позвонить клиенту. Сделка #10 (https://crm.corp.ru/deal/10).\n" +
				"Ответственный: @" + p17.FullName() + ". [вложение]\n" +
				"• Уточнить ТЗ• Выставить счёт",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanBBCode(tc.in, a)
			if got != tc.want {
				t.Errorf("CleanBBCode() =\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestImport_ValidationErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("missing users.tsv", func(t *testing.T) {
		dir := t.TempDir()
		opts := Options{Dir: dir}
		st, err := Import(ctx, nil, opts)
		if err == nil {
			t.Fatal("expected error for missing users.tsv, got nil")
		}
		if st != (Stats{}) {
			t.Fatalf("expected empty stats on error, got %+v", st)
		}
	})

	t.Run("invalid users.tsv column count", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "users.tsv"), []byte("1\t2\t3\t4\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := Options{Dir: dir}
		_, err := Import(ctx, nil, opts)
		if err == nil || !strings.Contains(err.Error(), "columns, want 7") {
			t.Fatalf("expected 7 columns error, got: %v", err)
		}
	})

	t.Run("missing tasks.tsv", func(t *testing.T) {
		dir := t.TempDir()
		userRow := "1\t" + e("Name") + "\t" + e("Last") + "\t\tM\t" + e("mail@example.com") + "\t" + e("login") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "users.tsv"), []byte(userRow), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := Options{Dir: dir}
		_, err := Import(ctx, nil, opts)
		if err == nil {
			t.Fatal("expected error for missing tasks.tsv, got nil")
		}
	})

	t.Run("invalid tasks.tsv column count", func(t *testing.T) {
		dir := t.TempDir()
		userRow := "1\t" + e("Name") + "\t" + e("Last") + "\t\tM\t" + e("mail@example.com") + "\t" + e("login") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "users.tsv"), []byte(userRow), 0o600); err != nil {
			t.Fatal(err)
		}
		// tasks.tsv requires 16 columns; write 10
		if err := os.WriteFile(filepath.Join(dir, "tasks.tsv"), []byte("1\t2\t3\t4\t5\t6\t7\t8\t9\t10\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := Options{Dir: dir}
		_, err := Import(ctx, nil, opts)
		if err == nil || !strings.Contains(err.Error(), "columns, want 16") {
			t.Fatalf("expected 16 columns error, got: %v", err)
		}
	})

	t.Run("missing members.tsv", func(t *testing.T) {
		dir := t.TempDir()
		userRow := "1\t" + e("Name") + "\t" + e("Last") + "\t\tM\t" + e("mail@example.com") + "\t" + e("login") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "users.tsv"), []byte(userRow), 0o600); err != nil {
			t.Fatal(err)
		}
		// Write valid 16-column tasks row
		taskCols := make([]string, 16)
		for i := range taskCols {
			taskCols[i] = "val"
		}
		if err := os.WriteFile(filepath.Join(dir, "tasks.tsv"), []byte(strings.Join(taskCols, "\t")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := Options{Dir: dir}
		_, err := Import(ctx, nil, opts)
		if err == nil {
			t.Fatal("expected error for missing members.tsv, got nil")
		}
	})

	t.Run("invalid members.tsv column count", func(t *testing.T) {
		dir := t.TempDir()
		userRow := "1\t" + e("Name") + "\t" + e("Last") + "\t\tM\t" + e("mail@example.com") + "\t" + e("login") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "users.tsv"), []byte(userRow), 0o600); err != nil {
			t.Fatal(err)
		}
		taskCols := make([]string, 16)
		for i := range taskCols {
			taskCols[i] = "val"
		}
		if err := os.WriteFile(filepath.Join(dir, "tasks.tsv"), []byte(strings.Join(taskCols, "\t")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// members.tsv requires 3 columns; write 2
		if err := os.WriteFile(filepath.Join(dir, "members.tsv"), []byte("101\t25\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := Options{Dir: dir}
		_, err := Import(ctx, nil, opts)
		if err == nil || !strings.Contains(err.Error(), "columns, want 3") {
			t.Fatalf("expected 3 columns error, got: %v", err)
		}
	})

	t.Run("missing comments.tsv", func(t *testing.T) {
		dir := t.TempDir()
		userRow := "1\t" + e("Name") + "\t" + e("Last") + "\t\tM\t" + e("mail@example.com") + "\t" + e("login") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "users.tsv"), []byte(userRow), 0o600); err != nil {
			t.Fatal(err)
		}
		taskCols := make([]string, 16)
		for i := range taskCols {
			taskCols[i] = "val"
		}
		if err := os.WriteFile(filepath.Join(dir, "tasks.tsv"), []byte(strings.Join(taskCols, "\t")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "members.tsv"), []byte("101\t25\tO\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := Options{Dir: dir}
		_, err := Import(ctx, nil, opts)
		if err == nil {
			t.Fatal("expected error for missing comments.tsv, got nil")
		}
	})

	t.Run("invalid comments.tsv column count", func(t *testing.T) {
		dir := t.TempDir()
		userRow := "1\t" + e("Name") + "\t" + e("Last") + "\t\tM\t" + e("mail@example.com") + "\t" + e("login") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "users.tsv"), []byte(userRow), 0o600); err != nil {
			t.Fatal(err)
		}
		taskCols := make([]string, 16)
		for i := range taskCols {
			taskCols[i] = "val"
		}
		if err := os.WriteFile(filepath.Join(dir, "tasks.tsv"), []byte(strings.Join(taskCols, "\t")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "members.tsv"), []byte("101\t25\tO\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// comments.tsv requires 5 columns; write 3
		if err := os.WriteFile(filepath.Join(dir, "comments.tsv"), []byte("1\t101\t17\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := Options{Dir: dir}
		_, err := Import(ctx, nil, opts)
		if err == nil || !strings.Contains(err.Error(), "columns, want 5") {
			t.Fatalf("expected 5 columns error, got: %v", err)
		}
	})
}
