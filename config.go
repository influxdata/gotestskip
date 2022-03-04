package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

type skipConfig struct {
	Packages map[string]map[string]bool `json:"packages"`
}

func readSkipConfig(file string) (_ *skipConfig, err error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pkg := ""
	skip := &skipConfig{
		Packages: make(map[string]map[string]bool),
	}
	scan := bufio.NewScanner(f)
	line := 1
	defer func() {
		if err != nil {
			err = fmt.Errorf("line %d: %v", line, err)
		}
	}()
	for ; scan.Scan(); line++ {
		line := strings.TrimSpace(scan.Text())
		if strings.HasSuffix(line, ":") {
			line = line[:len(line)-1]
			if line == "" {
				return nil, fmt.Errorf("empty package line")
			}
			pkg = line
			if skip.Packages[pkg] == nil {
				skip.Packages[pkg] = make(map[string]bool)
			}
			continue
		}
		if pkg == "" {
			return nil, fmt.Errorf("no package specified")
		}
		fields := strings.Fields(line)
		if len(fields) != 1 && len(fields) != 2 {
			return nil, fmt.Errorf("too many fields on test line")
		}
		skip.Packages[pkg][fields[0]] = true
		if len(fields) == 2 {
			// Check that the date looks valid but don't do anything with it.
			if _, err := time.Parse("2006-01-02", fields[1]); err != nil {
				return nil, fmt.Errorf("invalid date on test line %q: %v", line, err)
			}
		}
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("error reading %q: %v", file, err)
	}
	return skip, nil
}
