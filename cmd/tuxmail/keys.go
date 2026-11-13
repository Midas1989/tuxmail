// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Midas1989/Mailclient/internal/instance"
)

// keys manages the key ring of the server's stored secrets (internal/auth/keys.go).
func (c *cli) keys(ctx context.Context, args []string) error {
	if len(args) == 0 {
		c.usage()
		return fmt.Errorf("%w: missing keys subcommand", errUsage)
	}
	sub, args := args[0], args[1:]
	fs, cfgPath := newFlagSet("keys "+sub, c)
	kid := fs.String("kid", "", "key id (retire)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	yes := fs.Bool("yes", false, "confirm (rollback)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fmt.Errorf("%w: keys %s takes no arguments", errUsage, sub)
	}
	s, ctx, err := openService(ctx, *cfgPath)
	if err != nil {
		return err
	}
	defer s.Close()
	switch sub {
	case "status":
		st, err := s.KeyStatus(ctx)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(c.stdout).Encode(st)
		}
		kids := make([]string, 0, len(st.Kids))
		for k := range st.Kids {
			kids = append(kids, k)
		}
		sort.Strings(kids)
		for _, k := range kids {
			mark := ""
			if k == st.Primary {
				mark = " (primary)"
			}
			fmt.Fprintf(c.stdout, "key %s%s: %d values\n", k, mark, st.Kids[k])
		}
		fmt.Fprintf(c.stdout, "old format (master.key): %d values, %d team vaults; master.key present: %v\n", st.Legacy, st.TeamVaultsPending, st.MasterKeyPresent)
		fmt.Fprintf(c.stdout, "waiting for keys rewrap: %d\n", st.NeedsRewrap())
		if st.UnknownKid > 0 {
			return fmt.Errorf("%d values use a key that is not in the ring: restore secretbox.json from a backup", st.UnknownKid)
		}
		return nil
	case "rotate":
		k, err := s.RotateKey(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "new primary key %s; back up secretbox.json, then run: tuxmail keys rewrap\n", k)
		return nil
	case "rewrap":
		rep, err := s.RewrapSecrets(ctx)
		if rep != nil {
			if *asJSON {
				if jerr := json.NewEncoder(c.stdout).Encode(rep); jerr != nil {
					return jerr
				}
			} else {
				fmt.Fprintf(c.stdout, "converted %d, already current %d, changed meanwhile %d, failed %d, team vaults %d\n",
					rep.Converted, rep.Current, rep.Changed, rep.Failed, rep.Teams)
			}
		}
		if err == nil && rep.Changed > 0 {
			fmt.Fprintln(c.stdout, "some values changed meanwhile: run keys rewrap again")
		}
		return err
	case "retire":
		if *kid == "" {
			return fmt.Errorf("%w: keys retire needs --kid", errUsage)
		}
		if err := s.RetireKey(ctx, *kid); err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "key %s removed from the ring\n", *kid)
		return nil
	case "retire-legacy":
		dst, err := s.RetireLegacyKey(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "master.key moved to %s; keep it with the old backups, it is no longer needed here (rollback is no longer possible)\n", dst)
		return nil
	case "rollback":
		if !*yes {
			return fmt.Errorf("%w: keys rollback changes the database for the previous program version; stop the service, back up, then pass --yes", errUsage)
		}
		// serve holds this lock while it runs.
		lock, err := instance.AcquireWait(s.Config().DataDir, 0)
		if err != nil {
			return fmt.Errorf("the data directory is in use, stop the service first: %w", err)
		}
		defer lock.Release()
		rep, err := s.RollbackToLegacy(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.stdout, "rolled back: start the previous program version now, not this one")
		if rep.RecoveryCodesRemoved > 0 {
			fmt.Fprintf(c.stdout, "%d recovery codes made since the upgrade are gone; these accounts must generate new ones: %s\n",
				rep.RecoveryCodesRemoved, strings.Join(rep.UsersWithoutCodes, ", "))
		}
		if rep.ScopedTokensRevoked > 0 {
			fmt.Fprintf(c.stdout, "%d device tokens with restricted scopes were revoked (the previous version ignores scopes); those devices must sign in again\n", rep.ScopedTokensRevoked)
		}
		return nil
	default:
		c.usage()
		return fmt.Errorf("%w: unknown keys subcommand %q", errUsage, sub)
	}
}

// audit checks the audit chain of the server.
func (c *cli) audit(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "verify" {
		c.usage()
		return fmt.Errorf("%w: use audit verify", errUsage)
	}
	fs, cfgPath := newFlagSet("audit verify", c)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s, ctx, err := openService(ctx, *cfgPath)
	if err != nil {
		return err
	}
	defer s.Close()
	rep, err := s.VerifyAuditChain(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		if err := json.NewEncoder(c.stdout).Encode(rep); err != nil {
			return err
		}
	} else if rep.OK() {
		fmt.Fprintf(c.stdout, "audit chain intact: %d entries, newest hash %s\n", rep.Entries, rep.Head)
	}
	if !rep.OK() {
		return fmt.Errorf("audit chain broken at entry %d: %s", rep.BrokenAt, rep.Reason)
	}
	return nil
}
