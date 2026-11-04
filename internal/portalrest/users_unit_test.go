package portalrest

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFlexStr(t *testing.T) {
	for _, tc := range []struct {
		name string
		json string
		want flexStr
	}{
		{"regular string", `"hello world"`, "hello world"},
		{"empty string", `""`, ""},
		{"integer number", `12345`, "12345"},
		{"float number", `123.45`, "123.45"},
		{"boolean true", `true`, "true"},
		{"boolean false", `false`, "false"},
		{"json null", `null`, ""},
		{"string containing null", `"null"`, "null"},
		{"string containing whitespace", `"  spaces  "`, "  spaces  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got flexStr
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", tc.json, err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFlexInt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		json    string
		want    flexInt
		wantErr bool
	}{
		{"json number", `42`, 42, false},
		{"zero number", `0`, 0, false},
		{"negative number", `-15`, -15, false},
		{"quoted number", `"42"`, 42, false},
		{"quoted zero", `"0"`, 0, false},
		{"quoted negative", `"-99"`, -99, false},
		{"invalid string", `"not-a-number"`, 0, true},
		{"empty string", `""`, 0, true},
		{"json null fails strconv", `null`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got flexInt
			err := json.Unmarshal([]byte(tc.json), &got)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("json.Unmarshal(%s) expected error, got nil", tc.json)
				}
				return
			}
			if err != nil {
				t.Fatalf("json.Unmarshal(%s) unexpected error = %v", tc.json, err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestUserJSON(t *testing.T) {
	// the portal user.get returns fields in diverse shapes (inner phone as int or string,
	// departments as []int or []string, dates in ISO 8601).
	raw := `{
		"ID": "42",
		"ACTIVE": true,
		"USER_TYPE": "employee",
		"NAME": "Иван",
		"LAST_NAME": "Иванов",
		"SECOND_NAME": "Иванович",
		"EMAIL": "ivan@example.com",
		"PERSONAL_GENDER": "M",
		"PERSONAL_BIRTHDAY": "1990-05-15T00:00:00+03:00",
		"PERSONAL_CITY": "Москва",
		"PERSONAL_MOBILE": "+79991234567",
		"PERSONAL_PHONE": "123-45",
		"WORK_PHONE": "+74950001122",
		"UF_PHONE_INNER": 1540,
		"WORK_POSITION": "Инженер",
		"UF_EMPLOYMENT_DATE": "2021-03-01",
		"TIME_ZONE": "Europe/Moscow",
		"UF_DEPARTMENT": [1, "5", 12]
	}`

	var u User
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("Unmarshal(User) failed: %v", err)
	}

	if u.ID != "42" {
		t.Errorf("ID = %q, want 42", u.ID)
	}
	if !u.Active {
		t.Errorf("Active = false, want true")
	}
	if u.Type != "employee" {
		t.Errorf("Type = %q, want employee", u.Type)
	}
	if u.Name != "Иван" || u.LastName != "Иванов" || u.SecondName != "Иванович" {
		t.Errorf("Name = %q %q %q", u.Name, u.LastName, u.SecondName)
	}
	if u.Email != "ivan@example.com" {
		t.Errorf("Email = %q", u.Email)
	}
	if u.Gender != "M" {
		t.Errorf("Gender = %q, want M", u.Gender)
	}
	if u.City != "Москва" {
		t.Errorf("City = %q, want Москва", u.City)
	}
	if u.Mobile != "+79991234567" || u.WorkPhone != "+74950001122" || u.Phone != "123-45" {
		t.Errorf("Phones: mobile=%q work=%q phone=%q", u.Mobile, u.WorkPhone, u.Phone)
	}
	if u.Inner != "1540" {
		t.Errorf("Inner = %q, want 1540", u.Inner)
	}
	if u.Position != "Инженер" {
		t.Errorf("Position = %q, want Инженер", u.Position)
	}
	if u.Employment != "2021-03-01" {
		t.Errorf("Employment = %q, want 2021-03-01", u.Employment)
	}
	if u.TimeZone != "Europe/Moscow" {
		t.Errorf("TimeZone = %q, want Europe/Moscow", u.TimeZone)
	}
	wantDepts := []flexInt{1, 5, 12}
	if !reflect.DeepEqual(u.Departments, wantDepts) {
		t.Errorf("Departments = %v, want %v", u.Departments, wantDepts)
	}
}

func TestDepartmentJSON(t *testing.T) {
	raw := `{
		"ID": "7",
		"NAME": "Отдел продаж",
		"SORT": 500,
		"PARENT": "1",
		"UF_HEAD": "42"
	}`

	var d Department
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("Unmarshal(Department) failed: %v", err)
	}
	if d.ID != 7 {
		t.Errorf("ID = %d, want 7", d.ID)
	}
	if d.Name != "Отдел продаж" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Sort != 500 {
		t.Errorf("Sort = %d, want 500", d.Sort)
	}
	if d.Parent != "1" {
		t.Errorf("Parent = %q, want 1", d.Parent)
	}
	if d.Head != "42" {
		t.Errorf("Head = %q, want 42", d.Head)
	}
}

func TestRNG(t *testing.T) {
	// Same seed and parts must produce identical pseudo-random stream
	r1 := rng("seed1", "partA", "partB")
	r2 := rng("seed1", "partA", "partB")

	var seq1, seq2 []int
	for i := 0; i < 10; i++ {
		seq1 = append(seq1, r1.IntN(1000))
		seq2 = append(seq2, r2.IntN(1000))
	}
	if !reflect.DeepEqual(seq1, seq2) {
		t.Errorf("rng is not deterministic: %v vs %v", seq1, seq2)
	}

	// Different seeds produce different sequences
	rDiffSeed := rng("seed2", "partA", "partB")
	var seqDiff []int
	for i := 0; i < 10; i++ {
		seqDiff = append(seqDiff, rDiffSeed.IntN(1000))
	}
	if reflect.DeepEqual(seq1, seqDiff) {
		t.Errorf("different seeds gave identical output: %v", seq1)
	}

	// Different parts produce different sequences
	rDiffPart := rng("seed1", "partA", "different")
	var seqDiffPart []int
	for i := 0; i < 10; i++ {
		seqDiffPart = append(seqDiffPart, rDiffPart.IntN(1000))
	}
	if reflect.DeepEqual(seq1, seqDiffPart) {
		t.Errorf("different parts gave identical output: %v", seq1)
	}
}

func TestAtoi(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"42", 42},
		{" 42 ", 42},
		{"\t-17\n", -17},
		{"0", 0},
		{"", 0},
		{"   ", 0},
		{"invalid", 0},
		{"123abc", 0},
	} {
		if got := atoi(tc.in); got != tc.want {
			t.Errorf("atoi(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want string
	}{
		{"empty list", nil, ""},
		{"all empty strings", []string{"", "  ", "\t\n "}, ""},
		{"first element non empty", []string{"alpha", "beta"}, "alpha"},
		{"second element non empty", []string{"", "beta", "gamma"}, "beta"},
		{"leading spaces skipped", []string{"   ", "  gamma  ", "delta"}, "  gamma  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstNonEmpty(tc.in...); got != tc.want {
				t.Errorf("firstNonEmpty(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
