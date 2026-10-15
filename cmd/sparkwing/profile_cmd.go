package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

type profileIndex struct {
	Name       string `json:"name"`
	Controller string `json:"controller"`
	Token      string `json:"token"`
}

func profileRecord(p *profile.Profile) profileIndex {
	return profileIndex{Name: p.Name, Controller: p.ControllerURL(), Token: redactToken(p.ControllerToken())}
}

func writeProfile(p *profile.Profile, format string) error {
	record := profileRecord(p)
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(record)
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, record.Name)
		return err
	}
	_, err := fmt.Fprintf(os.Stdout, "name:       %s\ncontroller: %s\ntoken:      %s\n", record.Name, record.Controller, record.Token)
	return err
}

func listProfiles(format string) error {
	cfg, _, err := loadCfg()
	if err != nil {
		return err
	}
	index := make([]profileIndex, 0, len(cfg.Profiles))
	for _, name := range cfg.Names() {
		index = append(index, profileRecord(cfg.Profiles[name]))
	}
	switch format {
	case "json":
		return ndjson.Write(os.Stdout, index)
	case "plain":
		for _, p := range index {
			if _, err := fmt.Fprintln(os.Stdout, p.Name); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tCONTROLLER\tTOKEN")
	for _, p := range index {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, emptyDash(p.Controller), p.Token)
	}
	return tw.Flush()
}
