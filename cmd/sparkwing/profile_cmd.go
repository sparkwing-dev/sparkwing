package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
)

type profileIndex struct {
	Name       string `json:"name"`
	Controller string `json:"controller"`
	Token      string `json:"token"`
	Default    bool   `json:"default"`
}

func profileRecord(p *namedProfile) profileIndex {
	return profileIndex{Name: p.Name, Controller: p.ControllerURL(), Token: redactToken(p.ControllerToken()), Default: p.Default}
}

func writeProfile(p *namedProfile, format string) error {
	record := profileRecord(p)
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(record)
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, record.Name)
		return err
	}
	_, err := fmt.Fprintf(os.Stdout, "name:       %s\ncontroller: %s\ntoken:      %s\ndefault:    %t\n", record.Name, record.Controller, record.Token, record.Default)
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
			if _, err := fmt.Fprintln(os.Stdout, p.Name); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tCONTROLLER\tTOKEN\tDEFAULT")
	for _, p := range index {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%t\n", p.Name, emptyDash(p.Controller), p.Token, p.Default)
	}
	return tw.Flush()
}
