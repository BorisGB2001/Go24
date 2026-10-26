// Command go24-import-portal imports a portal into go24 through an incoming webhook (read-only REST calls),
// replacing every personal detail with made-up data (see internal/portalrest). The webhook URL is a secret:
// it is read from standard input, never from flags or the environment, and never printed.
//
//	security find-generic-password -s go24-portal-webhook -w | go24-import-portal -portal acme -seed-file seed -what users
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"go24/internal/anon"
	"go24/internal/db"
	"go24/internal/portalrest"
)

func main() {
	portal := flag.String("portal", "", "short tag of the portal (part of imported users' identity), e.g. acme; required")
	seedFile := flag.String("seed-file", "", "file with the anonymization seed (keep it: re-imports give the same pseudonyms); required")
	what := flag.String("what", "users", "what to import, in order: users, departments (needs webhook scope \"department\"), tasks (after users), e.g. users,departments,tasks")
	root := flag.String("root-name", "Моя компания", "name of the company node if the structure does not exist yet")
	flag.Parse()
	if err := run(*portal, *seedFile, *what, *root); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func run(portal, seedFile, what, rootName string) error {
	if portal == "" || seedFile == "" {
		return errors.New("укажите -portal и -seed-file")
	}
	seedB, err := os.ReadFile(seedFile)
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	seed := strings.TrimSpace(string(seedB)) + ":" + portal
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("адрес вебхука ожидается на стандартном вводе")
	}
	client, err := portalrest.New(line)
	if err != nil {
		return err
	}
	fmt.Println("портал:", client.Host())

	ctx := context.Background()
	pool, err := db.Open(ctx, os.Getenv("GO24_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `INSERT INTO departments (name, sort) SELECT $1, 0
		WHERE NOT EXISTS (SELECT 1 FROM departments WHERE parent_id IS NULL)`, rootName); err != nil {
		return err
	}
	o := portalrest.Options{Portal: portal, Anon: anon.New(seed, portal+".import"), Seed: seed}
	steps := strings.Split(what, ",")
	for _, step := range steps {
		switch strings.TrimSpace(step) {
		case "users":
			st, err := portalrest.ImportUsers(ctx, client, pool, o)
			if err != nil {
				return err
			}
			fmt.Printf("сотрудников: %d (работают: %d), отделов связано: %d\n", st.Users, st.Active, st.Departments)
		case "tasks":
			st, err := portalrest.ImportTasks(ctx, client, pool, o)
			if err != nil {
				return err
			}
			fmt.Printf("проектов новых: %d, задач новых: %d, комментариев: %d, пунктов чек-листов: %d\n", st.Projects, st.Tasks, st.Comments, st.Checks)
		case "departments":
			n, err := portalrest.ImportDepartments(ctx, client, pool, o)
			if err != nil {
				return err
			}
			fmt.Println("отделов:", n)
		default:
			return fmt.Errorf("неизвестный шаг %q", step)
		}
	}
	return nil
}
