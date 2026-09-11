// inspect-org-membership reports only relationship identifiers for a Gogs organization.
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
	username := flag.String("user", "danfragoso", "Username")
	flag.Parse()
	if err := inspect(*configPath, *organization, *username); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func inspect(configPath, organization, username string) error {
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
	ownerTeam, err := org.GetOwnerTeam()
	if err != nil {
		return errors.Wrap(err, "find owners team")
	}
	memberships, err := database.GetOrgUsersByOrgID(org.ID, 0)
	if err != nil {
		return errors.Wrap(err, "list organization memberships")
	}
	if err := ownerTeam.GetMembers(); err != nil {
		return errors.Wrap(err, "list owners team members")
	}
	fmt.Printf("organization_id=%d user_id=%d owners_team_id=%d\n", org.ID, user.ID, ownerTeam.ID)
	fmt.Printf("org_user_rows=%d owners_team_member_rows=%d is_member=%t is_owner=%t is_owner_team_member=%t\n", len(memberships), len(ownerTeam.Members), org.IsOrgMember(user.ID), database.IsOrganizationOwner(org.ID, user.ID), ownerTeam.IsMember(user.ID))
	return nil
}
