// mirror-olive creates the public Olive mirror through Gogs' own database API.
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

const (
	defaultConfig = "/home/pizzadatabase/gogs/shared/custom/conf/app.ini"
	ownerName     = "database.pizza"
	repoName      = "olive"
	remoteURL     = "https://github.com/database-pizza/olive.git"
)

func main() {
	configPath := flag.String("config", defaultConfig, "Gogs custom configuration file")
	cleanupOnly := flag.Bool("cleanup-only", false, "Remove the incomplete Olive mirror record without cloning")
	flag.Parse()
	if err := mirror(*configPath, *cleanupOnly); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func mirror(configPath string, cleanupOnly bool) error {
	if err := conf.Init(configPath); err != nil {
		return errors.Wrap(err, "load Gogs configuration")
	}
	conf.InitLogging(true)
	if err := database.NewEngine(); err != nil {
		return errors.Wrap(err, "initialize Gogs database")
	}
	owner, err := database.GetOrgByName(ownerName)
	if err != nil {
		return errors.Wrapf(err, "find %q organization", ownerName)
	}
	if existing, err := database.GetRepositoryByName(owner.ID, repoName); err == nil {
		// MigrateRepository creates the database record before cloning. Recover only
		// this known mirror when its repository directory was never created.
		if !repositoryExists(existing.RepoPath()) {
			if err := database.DeleteRepository(owner.ID, existing.ID); err != nil {
				return errors.Wrap(err, "remove failed Olive mirror")
			}
			if cleanupOnly {
				fmt.Println("Removed incomplete Olive mirror record.")
				return nil
			}
		} else {
			fmt.Printf("Mirror already exists: https://git.database.pizza/%s/%s\n", ownerName, existing.Name)
			return nil
		}
	} else if cleanupOnly && database.IsErrRepoNotExist(err) {
		fmt.Println("No Olive mirror record exists.")
		return nil
	} else if !database.IsErrRepoNotExist(err) {
		return errors.Wrap(err, "check existing Olive mirror")
	}
	doer, err := database.Handle.Users().GetByUsername(context.Background(), "danfragoso")
	if err != nil {
		return errors.Wrap(err, "find mirror administrator")
	}
	repo, err := database.MigrateRepository(doer, owner, database.MigrateRepoOptions{
		Name:        repoName,
		Description: "Shared Svelte 5 component library for database.pizza.",
		IsMirror:    true,
		RemoteAddr:  remoteURL,
	})
	if err != nil {
		return errors.Wrap(err, "mirror Olive")
	}
	fmt.Printf("Mirrored Olive: https://git.database.pizza/%s/%s\n", ownerName, repo.Name)
	return nil
}

func repositoryExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
