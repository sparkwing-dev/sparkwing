package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
)

type profileIndex struct {
	Name           string     `json:"name"`
	Controller     string     `json:"controller"`
	Token          string     `json:"token"`
	Default        bool       `json:"default"`
	AdminExpiresAt *time.Time `json:"admin_expires_at,omitempty"`
}

func profileRecord(p *namedProfile) profileIndex {
	record := profileIndex{Name: p.Name, Controller: p.ControllerURL(), Token: redactToken(p.MemberToken()), Default: p.Default}
	if p.liveAdminToken(time.Now()) != "" {
		record.AdminExpiresAt = p.Controller.AdminExpiresAt
	}
	return record
}

func writeProfile(p *namedProfile, format string) error {
	record := profileRecord(p)
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(record)
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, opsview.DisplayText(record.Name))
		return err
	}
	admin := "none"
	if record.AdminExpiresAt != nil {
		admin = "team.admin until " + record.AdminExpiresAt.Local().Format(time.RFC3339)
	}
	_, err := fmt.Fprintf(os.Stdout, "name:       %s\ncontroller: %s\ntoken:      %s\nadmin:      %s\ndefault:    %t\n", opsview.DisplayText(record.Name), opsview.DisplayText(record.Controller), opsview.DisplayText(record.Token), admin, record.Default)
	return err
}

func listProfiles(format string) error {
	profiles, _, err := loadProfiles()
	if err != nil {
		return err
	}
	index := make([]profileIndex, 0, len(profiles))
	for _, name := range profileNames(profiles) {
		index = append(index, profileRecord(&namedProfile{Name: name, Profile: profiles[name]}))
	}
	switch format {
	case "json":
		return ndjson.Write(os.Stdout, index)
	case "plain":
		for _, p := range index {
			if _, err := fmt.Fprintln(os.Stdout, opsview.DisplayText(p.Name)); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tCONTROLLER\tTOKEN\tDEFAULT")
	for _, p := range index {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%t\n", opsview.DisplayText(p.Name), opsview.DisplayText(emptyDash(p.Controller)), opsview.DisplayText(p.Token), p.Default)
	}
	return tw.Flush()
}
