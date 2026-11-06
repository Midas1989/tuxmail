// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
)

func (c *cli) user(ctx context.Context, args []string) error {
	if len(args) == 0 {
		c.usage()
		return fmt.Errorf("%w: missing user subcommand", errUsage)
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "add":
		return c.userAdd(ctx, args)
	case "list":
		return c.userList(ctx, args)
	case "disable", "enable":
		return c.userSimple(ctx, sub, args, func(ctx context.Context, s *auth.Service, u *auth.User) error {
			if err := s.SetDisabled(ctx, u.ID, sub == "disable"); err != nil {
				return err
			}
			fmt.Fprintf(c.stdout, "user %s %sd\n", u.Username, sub)
			return nil
		})
	case "reset-2fa":
		return c.userSimple(ctx, sub, args, func(ctx context.Context, s *auth.Service, u *auth.User) error {
			if err := s.ResetSecondFactors(ctx, u.ID); err != nil { // TOTP, recovery codes and passkeys
				return err
			}
			fmt.Fprintf(c.stdout, "two-factor authentication of %s reset (TOTP, recovery codes and passkeys removed)\n", u.Username)
			return nil
		})
	case "reset-password":
		return c.userResetPassword(ctx, args)
	case "set-role":
		return c.userSetRole(ctx, args)
	case "delete":
		return c.userDelete(ctx, args)
	default:
		c.usage()
		return fmt.Errorf("%w: unknown user subcommand %q", errUsage, sub)
	}
}

// openService loads the configuration and opens the user database.
func openService(ctx context.Context, path string) (*auth.Service, context.Context, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	s, err := auth.Open(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return s, auth.WithActor(ctx, auth.Actor{Name: "cli"}), nil
}

// readPassword reads a password from r (first line, without line ending).
func readPassword(r io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(r, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return "", errors.New("no password on standard input")
	}
	return pw, nil
}

func (c *cli) userAdd(ctx context.Context, args []string) error {
	fs, cfgPath := newFlagSet("user add", c)
	username := fs.String("username", "", "login name (required)")
	email := fs.String("email", "", "email address")
	display := fs.String("display-name", "", "display name")
	admin := fs.Bool("admin", false, "create an administrator (same as --role admin)")
	role := fs.String("role", "user", "role: admin, user or readonly")
	pwStdin := fs.Bool("password-stdin", false, "read the password from standard input")
	ifNotExists := fs.Bool("if-not-exists", false, "succeed without changes if the user already exists")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if *username == "" && len(pos) == 1 {
		*username, pos = pos[0], nil
	}
	if *username == "" || len(pos) > 0 {
		return fmt.Errorf("%w: user add needs --username", errUsage)
	}
	r := auth.Role(*role)
	if *admin {
		r = auth.RoleAdmin
	}
	// Read stdin before touching the database so a pipe is always consumed.
	var password string
	if *pwStdin {
		if password, err = readPassword(c.stdin); err != nil {
			return err
		}
	}
	s, ctx, err := openService(ctx, *cfgPath)
	if err != nil {
		return err
	}
	defer s.Close()
	if existing, err := s.GetUserByName(ctx, *username); err == nil && strings.EqualFold(existing.Username, *username) {
		if *ifNotExists {
			if *asJSON {
				return writeJSON(c.stdout, map[string]any{"created": false, "user": existing})
			}
			fmt.Fprintf(c.stdout, "user %s already exists, nothing to do\n", existing.Username)
			return nil
		}
		return fmt.Errorf("user %s already exists", existing.Username)
	}
	generated := password == ""
	if generated {
		password = auth.GeneratePassword(max(s.Config().Auth.PasswordMinLength, 20))
	}
	u, err := s.CreateUser(ctx, auth.NewUser{Username: *username, Email: *email, DisplayName: *display, Role: r, Password: password})
	if err != nil {
		return err
	}
	if *asJSON {
		out := map[string]any{"created": true, "user": u}
		if generated {
			out["password"] = password
		}
		return writeJSON(c.stdout, out)
	}
	fmt.Fprintf(c.stdout, "created user %s (id %d, role %s)\n", u.Username, u.ID, u.Role)
	if generated {
		fmt.Fprintf(c.stdout, "password: %s\n", password)
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (c *cli) userList(ctx context.Context, args []string) error {
	fs, cfgPath := newFlagSet("user list", c)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, ctx, err := openService(ctx, *cfgPath)
	if err != nil {
		return err
	}
	defer s.Close()
	users, err := s.ListUsers(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		if users == nil {
			users = []*auth.User{}
		}
		return writeJSON(c.stdout, users)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tUSERNAME\tEMAIL\tROLE\tSTATUS\t2FA\tLOGIN\tLAST LOGIN")
	for _, u := range users {
		status, twofa, login, last := "active", "-", "password", "never"
		if u.Disabled {
			status = "disabled"
		}
		if u.TOTPEnabled {
			twofa = "totp"
		}
		if u.OIDC {
			login = "oidc"
			if u.HasPassword {
				login = "password+oidc"
			}
		}
		if !u.LastLoginAt.IsZero() {
			last = u.LastLoginAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", u.ID, u.Username, u.Email, u.Role, status, twofa, login, last)
	}
	return tw.Flush()
}

// userSimple handles commands that take just a username.
func (c *cli) userSimple(ctx context.Context, name string, args []string, fn func(context.Context, *auth.Service, *auth.User) error) error {
	fs, cfgPath := newFlagSet("user "+name, c)
	username := fs.String("username", "", "login name")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if *username == "" && len(pos) == 1 {
		*username = pos[0]
	} else if *username == "" || len(pos) > 0 {
		return fmt.Errorf("%w: user %s needs exactly one USERNAME", errUsage, name)
	}
	s, ctx, err := openService(ctx, *cfgPath)
	if err != nil {
		return err
	}
	defer s.Close()
	u, err := s.GetUserByName(ctx, *username)
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			return fmt.Errorf("user %s not found", *username)
		}
		return err
	}
	return fn(ctx, s, u)
}

func (c *cli) userResetPassword(ctx context.Context, args []string) error {
	fs, cfgPath := newFlagSet("user reset-password", c)
	username := fs.String("username", "", "login name")
	pwStdin := fs.Bool("password-stdin", false, "read the new password from standard input")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if *username == "" && len(pos) == 1 {
		*username = pos[0]
	} else if *username == "" || len(pos) > 0 {
		return fmt.Errorf("%w: user reset-password needs exactly one USERNAME", errUsage)
	}
	var password string
	if *pwStdin {
		if password, err = readPassword(c.stdin); err != nil {
			return err
		}
	}
	return c.userSimple(ctx, "reset-password", []string{"--config", *cfgPath, *username}, func(ctx context.Context, s *auth.Service, u *auth.User) error {
		if password != "" {
			if err := s.SetPassword(ctx, u.ID, password, 0); err != nil {
				return err
			}
			fmt.Fprintf(c.stdout, "password of %s changed; all sessions revoked\n", u.Username)
			return nil
		}
		pw, err := s.ResetPassword(ctx, u.ID)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "password of %s reset; all sessions revoked\npassword: %s\n", u.Username, pw)
		return nil
	})
}

func (c *cli) userSetRole(ctx context.Context, args []string) error {
	fs, cfgPath := newFlagSet("user set-role", c)
	username := fs.String("username", "", "login name")
	role := fs.String("role", "", "admin, user or readonly")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if *username == "" && len(pos) > 0 {
		*username, pos = pos[0], pos[1:]
	}
	if *role == "" && len(pos) > 0 {
		*role, pos = pos[0], pos[1:]
	}
	if *username == "" || *role == "" || len(pos) > 0 {
		return fmt.Errorf("%w: usage: tuxmail user set-role USERNAME ROLE", errUsage)
	}
	return c.userSimple(ctx, "set-role", []string{"--config", *cfgPath, *username}, func(ctx context.Context, s *auth.Service, u *auth.User) error {
		if err := s.SetRole(ctx, u.ID, auth.Role(*role)); err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "role of %s set to %s\n", u.Username, *role)
		return nil
	})
}

func (c *cli) userDelete(ctx context.Context, args []string) error {
	fs, cfgPath := newFlagSet("user delete", c)
	username := fs.String("username", "", "login name")
	yes := fs.Bool("yes", false, "confirm deletion (required)")
	keep := fs.Bool("keep-data", false, "keep the data directory data_dir/users/<id>")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if *username == "" && len(pos) == 1 {
		*username = pos[0]
	} else if *username == "" || len(pos) > 0 {
		return fmt.Errorf("%w: user delete needs exactly one USERNAME", errUsage)
	}
	if !*yes {
		return fmt.Errorf("%w: deleting %s removes the user and its data; repeat with --yes", errUsage, *username)
	}
	return c.userSimple(ctx, "delete", []string{"--config", *cfgPath, *username}, func(ctx context.Context, s *auth.Service, u *auth.User) error {
		if err := s.DeleteUser(ctx, u.ID, !*keep); err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "user %s deleted\n", u.Username)
		return nil
	})
}
