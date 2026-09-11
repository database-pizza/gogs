package cmd

import (
	"os"
	"strings"

	"github.com/cockroachdb/errors"
)

// readPasswordFile returns the password stored in the file at path. Trailing
// newline characters are trimmed but all other whitespace is preserved because
// it is significant. The file must not be empty and must not be accessible by
// group or others.
func readPasswordFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", errors.Wrap(err, "stat password file")
	}
	if info.IsDir() {
		return "", errors.Newf("password file %q is a directory", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", errors.Newf("password file %q has insecure permissions %o, it must not be accessible by group or others", path, perm)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.Wrap(err, "read password file")
	}

	password := strings.TrimRight(string(data), "\n")
	if password == "" {
		return "", errors.Newf("password file %q is empty", path)
	}
	return password, nil
}

// resolvePassword returns the password to use when creating a user. Exactly one
// of hasPassword and hasPasswordFile must be true, otherwise it returns an error.
func resolvePassword(password, passwordFile string, hasPassword, hasPasswordFile bool) (string, error) {
	if hasPassword == hasPasswordFile {
		return "", errors.New("exactly one of --password or --password-file must be set")
	}
	if hasPasswordFile {
		password, err := readPasswordFile(passwordFile)
		if err != nil {
			return "", err
		}
		return password, nil
	}
	return password, nil
}
