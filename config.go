package main

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type skipConfig struct {
	Packages map[string]map[string]bool `json:"packages"`
}

func readSkipConfig(file string, rootPkg string) (_ *skipConfig, err error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var pkgs map[string][]string
	if err := yaml.Unmarshal(data, &pkgs); err != nil {
		return nil, fmt.Errorf("cannot unmarshal skip config in %q: %v", file, err)
	}
	skip := &skipConfig{
		Packages: make(map[string]map[string]bool),
	}
	for pkg, tests := range pkgs {
		if rootPkg != "" {
			pkg = path.Join(rootPkg, pkg)
		}
		pm := make(map[string]bool)
		for _, test := range tests {
			fields := strings.Fields(test)
			if len(fields) != 1 && len(fields) != 2 {
				return nil, fmt.Errorf("too many fields in test name")
			}
			pm[fields[0]] = true
			if len(fields) == 2 {
				// Check that the date looks valid but don't do anything with it.
				if _, err := time.Parse("2006-01-02", fields[1]); err != nil {
					return nil, fmt.Errorf("invalid date on test %q: %q", test, err)
				}
			}
			pm[test] = true
		}
		skip.Packages[pkg] = pm
	}
	return skip, nil
}
