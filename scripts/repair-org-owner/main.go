// repair-org-owner restores the owner-team membership for an existing organization.
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
	organization := flag.String("org", "database.pizza", "Organization name")
	username := flag.String("user", "danfragoso", "Organization owner username")
	flag.Parse()
	if err := repair(*configPath, *organization, *username); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func repair(configPath, organization, username string) error {
	if err := conf.Init(configPath); err != nil {
		return errors.Wrap(err, "load Gogs configuration")
	}
	conf.InitLogging(true)
	if err := database.NewEngine(); err != nil {
		return errors.Wrap(err, "initialize Gogs database")
	}
	org, err := database.GetOrgByName(organization)
	if err != nil {
		return errors.Wrap(err, "find organization")
	}
	user, err := database.Handle.Users().GetByUsername(context.Background(), username)
	if err != nil {
		return errors.Wrap(err, "find user")
	}
	owners, err := org.GetOwnerTeam()
	if err != nil {
		return errors.Wrap(err, "find organization owners team")
	}
	if err := owners.AddMember(user.ID); err != nil {
		return errors.Wrap(err, "add user to organization owners team")
	}
	if !database.IsOrganizationOwner(org.ID, user.ID) {
		return errors.New("organization owner membership did not persist")
	}
	orgs, err := database.Handle.Organizations().List(context.Background(), database.ListOrgsOptions{
		MemberID:              user.ID,
		IncludePrivateMembers: true,
	})
	if err != nil {
		return errors.Wrap(err, "verify dashboard organization list")
	}
	for _, found := range orgs {
		if found.ID == org.ID {
			fmt.Printf("Organization owner membership verified: %s\n", organization)
			return nil
		}
	}
	return errors.New("organization membership is absent from dashboard list")
}
