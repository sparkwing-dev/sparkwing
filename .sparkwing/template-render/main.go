package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparks-core/templates"
)

func main() {
	if err := render(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func render() error {
	registry, err := templates.List()
	if err != nil {
		return err
	}
	rendered := make(map[string]templates.Template, len(registry))
	for _, tmpl := range registry {
		m := tmpl.Manifest
		params := make(map[string]string, len(m.VerifyParams)+1)
		for key, value := range m.VerifyParams {
			params[key] = value
		}
		for _, p := range m.Parameters {
			if p.Name == "pipeline-name" {
				params[p.Name] = m.Name
			}
		}
		tmpl.Body, err = templates.Render(m.Name, params)
		if err != nil {
			return err
		}
		tmpl.ReadMe = ""
		rendered[m.Name] = tmpl
	}
	return json.NewEncoder(os.Stdout).Encode(rendered)
}
