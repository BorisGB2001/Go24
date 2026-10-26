// Command go24-import-legacy imports a portal tasks export (see docs/import-legacy.md) into go24,
// replacing people's names, phones and e-mails with stable pseudonyms.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/anon"
	"go24/internal/db"
	"go24/internal/legacyimport"
)

func main() {
	dir := flag.String("dir", "/import", "directory with users.tsv, tasks.tsv, members.tsv, comments.tsv")
	key := flag.String("project", "EXT", "project key for imported tasks")
	name := flag.String("name", "корпоративного портала (импорт)", "project name")
	owner := flag.String("owner", "", "e-mail of the go24 user who owns the project (default: the first user who logged in)")
	tz := flag.String("tz", "Europe/Moscow", "time zone of Legacy dates")
	seed := flag.String("seed", "", "anonymization seed (keep it to get the same pseudonyms on re-import); required")
	blueprint := flag.String("authentik-blueprint", "", "after import, write an Authentik blueprint with the imported users to this file")
	onlyBlueprint := flag.Bool("only-blueprint", false, "skip the import, only (re)write the Authentik blueprint from the go24 database")
	onlyFiles := flag.Bool("files", false, "import attachments from files.tsv as empty placeholders (name and type only), into the built-in storage")
	flag.Parse()

	if *onlyFiles {
		if err := runFiles(*dir, *tz, *seed); err != nil {
			fmt.Fprintln(os.Stderr, "ошибка:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*dir, *key, *name, *owner, *tz, *seed, *blueprint, *onlyBlueprint); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

// runFiles imports attachments as empty placeholders; documents cannot be anonymized, so no content is copied.
func runFiles(dir, tz, seed string) error {
	if seed == "" {
		return errors.New("укажите -seed: тот же, что при импорте задач")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, os.Getenv("GO24_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	n, err := legacyimport.ImportFiles(ctx, pool, nil, legacyimport.Options{Dir: dir, Location: loc, Anon: anon.New(seed, "portal.import")})
	if err != nil {
		return err
	}
	fmt.Println("вложений добавлено (пустые, только имя и тип):", n)
	return nil
}

func run(dir, key, name, ownerEmail, tz, seed, blueprint string, onlyBlueprint bool) error {
	if onlyBlueprint {
		if blueprint == "" {
			return errors.New("укажите -authentik-blueprint")
		}
		ctx := context.Background()
		pool, err := db.Open(ctx, os.Getenv("GO24_DATABASE_URL"))
		if err != nil {
			return err
		}
		defer pool.Close()
		n, err := legacyimport.WriteAuthentikBlueprint(ctx, pool, blueprint)
		if err == nil {
			fmt.Println("blueprint для Authentik записан, пользователей:", n)
		}
		return err
	}
	if seed == "" {
		return errors.New("укажите -seed: по нему псевдонимы одинаковы при повторном импорте")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, os.Getenv("GO24_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	var ownerID string
	q := `SELECT id FROM users WHERE oidc_sub NOT LIKE 'legacy:%' ORDER BY created_at LIMIT 1`
	args := []any{}
	if ownerEmail != "" {
		q = `SELECT id FROM users WHERE email = $1 AND oidc_sub NOT LIKE 'legacy:%'`
		args = append(args, ownerEmail)
	}
	if err := pool.QueryRow(ctx, q, args...).Scan(&ownerID); errors.Is(err, pgx.ErrNoRows) {
		return errors.New("владелец проекта не найден: сначала войдите в go24 через Authentik")
	} else if err != nil {
		return err
	}

	st, err := legacyimport.Import(ctx, pool, legacyimport.Options{
		Dir: dir, ProjectKey: key, ProjectName: name, OwnerID: ownerID, Location: loc,
		Anon: anon.New(seed, "portal.import"),
	})
	if err != nil {
		return err
	}
	fmt.Println("импорт завершён:", st)
	if blueprint != "" {
		n, err := legacyimport.WriteAuthentikBlueprint(ctx, pool, blueprint)
		if err != nil {
			return err
		}
		fmt.Println("blueprint для Authentik записан, пользователей:", n)
	}
	return nil
}
