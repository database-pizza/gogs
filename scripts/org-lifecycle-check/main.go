// org-lifecycle-check creates and deletes a throwaway organization to verify
// that organization deletion does not hang. It leaves no organization behind.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/cockroachdb/errors"

	"gogs.io/gogs/internal/conf"
	"gogs.io/gogs/internal/database"
)

const defaultConfig = "/home/pizzadatabase/gogs/shared/custom/conf/app.ini"

func main() {
	configPath := flag.String("config", defaultConfig, "Gogs custom configuration file")
	owner := flag.String("owner", "danfragoso", "Owner username")
	flag.Parse()
	if err := run(*configPath, *owner); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(configPath, ownerName string) error {
	if err := conf.Init(configPath); err != nil {
		return errors.Wrap(err, "load Gogs configuration")
	}
	conf.InitLogging(true)
	if err := database.NewEngine(); err != nil {
		return errors.Wrap(err, "initialize Gogs database")
	}
	owner, err := database.Handle.Users().GetByUsername(context.Background(), ownerName)
	if err != nil {
		return errors.Wrap(err, "find owner")
	}
	name := fmt.Sprintf("delete-check-%d", time.Now().Unix())
	org := &database.User{Name: name, IsActive: true, Type: database.UserTypeOrganization}
	if err := database.CreateOrganization(org, owner); err != nil {
		return errors.Wrap(err, "create throwaway organization")
	}
	fmt.Printf("created %s (id=%d)\n", org.Name, org.ID)

	done := make(chan error, 1)
	go func() { done <- database.DeleteOrganization(org) }()
	select {
	case err := <-done:
		if err != nil {
			return errors.Wrap(err, "delete throwaway organization")
		}
	case <-time.After(20 * time.Second):
		return errors.New("DeleteOrganization did not finish within 20s (hang)")
	}
	fmt.Println("delete completed")

	if _, err := database.GetOrgByName(name); err == nil {
		return errors.New("organization still exists after delete")
	} else if !errors.Is(err, database.ErrOrgNotExist) {
		return errors.Wrap(err, "verify organization removal")
	}
	fmt.Println("organization removal verified")
	return nil
}
