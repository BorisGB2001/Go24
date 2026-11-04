package mail

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestPublicDomain(t *testing.T) {
	if !PublicDomain("Gmail.com") || !PublicDomain("yandex.ru") || PublicDomain("go24.test") {
		t.Error("PublicDomain")
	}
}

// TestMailTips runs against the test database: colleagues whose automatic replies are on (their text, go24's own
// one with the period's end, by an absence) are told of, the user and the inactive are not; the addresses outside the
// organization (go24's people's domains but mail services', the domain written from) are listed; what is not an
// address; a large audience.
func TestMailTips(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user := func(sub, name, email string, active bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, active) VALUES ($1, $2, $3, $4) RETURNING id`, sub, name, email, active).Scan(&id))
		return id
	}
	me, anna, petr, olga, gone := user("t1", "Я", "me@go24.test", true), user("t2", "Анна", "anna@go24.test", true),
		user("t3", "Пётр", "petr@gmail.com", true), user("t4", "Ольга", "olga@go24.test", true), user("t5", "Уволен", "gone@go24.test", false)
	s := NewStore(pool, "test-secret")
	s.Tr = func(key string, args ...any) string { return fmt.Sprintf(key+"|%v", args) }
	now := time.Now()
	end := now.Add(48 * time.Hour)
	must(s.SaveOOO(ctx, me, OOO{Mode: OOOOn, Internal: "Я сам в отпуске", Outside: "all"}))
	must(s.SaveOOO(ctx, anna, OOO{Mode: OOOOn, Internal: " В отпуске ", External: "Away", Outside: "all"}))
	must(s.SaveOOO(ctx, petr, OOO{Mode: OOOPeriod, From: &now, To: &end, Outside: "all"}))
	must(s.SaveOOO(ctx, olga, OOO{Mode: OOOOff, Absence: true, Internal: "Болею", Outside: "all"}))
	day := now.In(time.Local).Format("2006-01-02")
	_, err = pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by) VALUES ($1, 'sick', $2, $2, $1)`, olga, day)
	must(err)
	must(s.SaveOOO(ctx, gone, OOO{Mode: OOOOn, Internal: "Нет", Outside: "all"}))

	got, err := s.MailTips(ctx, me, "me@go24.test", []string{"me@go24.test", `"Анна" <ANNA@go24.test>`, "anna@go24.test", "petr@gmail.com",
		"olga@go24.test", "gone@go24.test", "boss@client.test", "friend@gmail.com", "Кто-то", "x@nodot"}, now)
	must(err)
	if got.Recipients != 10-1 || got.Large || !slices.Equal(got.External, []string{"boss@client.test", "friend@gmail.com"}) ||
		!slices.Equal(got.Invalid, []string{"Кто-то", "x@nodot"}) {
		t.Fatalf("tips: %+v", got)
	}
	if len(got.AutoReplies) != 3 {
		t.Fatalf("auto replies: %+v", got.AutoReplies)
	}
	a, p, o := got.AutoReplies[0], got.AutoReplies[1], got.AutoReplies[2]
	if a.UserID != anna || a.Name != "Анна" || a.Text != "В отпуске" || a.Until != nil {
		t.Errorf("anna: %+v", a)
	}
	if p.UserID != petr || p.Until == nil || p.Text != fmt.Sprintf("mail.ooo.default_until|[%s]", p.Until.In(time.Local).Format("format.date|[]")) {
		t.Errorf("petr: %+v", p)
	}
	if o.UserID != olga || o.Text != "Болею" || o.Until == nil {
		t.Errorf("olga: %+v", o)
	}

	// written from a domain of its own: it is the organization's too; a large audience
	var many []string
	for i := range LargeAudience + 1 {
		many = append(many, fmt.Sprintf("p%d@branch.test", i))
	}
	got, err = s.MailTips(ctx, me, "me@branch.test", many, now)
	must(err)
	if !got.Large || got.Recipients != LargeAudience+1 || len(got.External) != 0 || len(got.AutoReplies) != 0 {
		t.Errorf("large: %+v", got)
	}
	if got, err = s.MailTips(ctx, me, "", nil, now); err != nil || got.Recipients != 0 || got.External == nil {
		t.Errorf("none: %+v %v", got, err)
	}
}
