// query-org reproduces the Gogs dashboard organization query and prints only
// row counts and column names. It never prints row values or credentials.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/cockroachdb/errors"

	"gogs.io/gogs/internal/conf"
	"gogs.io/gogs/internal/database"
)

const defaultConfig = "/home/pizzadatabase/gogs/shared/custom/conf/app.ini"

func main() {
	configPath := flag.String("config", defaultConfig, "Gogs custom configuration file")
	flag.Parse()
	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	if err := conf.Init(configPath); err != nil {
		return errors.Wrap(err, "load Gogs configuration")
	}
	conf.InitLogging(true)
	db, err := database.SetEngine()
	if err != nil {
		return errors.Wrap(err, "initialize Gogs database")
	}

	orgs, err := database.Handle.Organizations().List(context.Background(), database.ListOrgsOptions{
		MemberID:              1,
		IncludePrivateMembers: true,
	})
	fmt.Printf("Organizations().List: count=%d err=%v\n", len(orgs), err)

	queries := []string{
		"SELECT * FROM `user` JOIN org_user ON org_user.org_id = user.id WHERE org_user.uid = ? ORDER BY user.id ASC",
		"SELECT * FROM `user` JOIN org_user ON org_user.org_id = `user`.id WHERE org_user.uid = ? ORDER BY `user`.id ASC",
		"SELECT user.* FROM user JOIN org_user ON org_user.org_id = user.id WHERE org_user.uid = ? ORDER BY user.id ASC",
		"SELECT * FROM org_user WHERE uid = ?",
	}
	for _, q := range queries {
		rows, err := db.Raw(q, 1).Rows()
		if err != nil {
			fmt.Printf("QUERY %q -> error: %v\n", q, err)
			continue
		}
		cols, _ := rows.Columns()
		count := 0
		for rows.Next() {
			count++
		}
		rows.Close()
		fmt.Printf("QUERY %q -> rows=%d cols=%v\n", q, count, cols)
	}

	return nil
}
