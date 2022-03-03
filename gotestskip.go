package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type skipJSON struct {
	Packages map[string]map[string]bool `json:"packages"`
}

func usage() {
	fmt.Fprintf(os.Stderr, `
usage: gotestskip [go test args...]

If $GO_SKIP_TESTS is empty or unset, this command functions
like "go test -json ..."; otherwise is tries to read the file named
by $GO_SKIP_TESTS, which should contain a JSON object with the following
schema:

{
	packages: [pkgName=_]: [string]: true
}

It can be run under gotestsum as:

	gotestsum --raw-command -- gotestskip ./...
`[1:])
}

// omitTime causes the JSON output to be made predictable
// making it easier to compare test output.
var predictableOutput = false

// event represents a go test -json event.
type event struct {
	Time    *time.Time `json:",omitempty"`
	Action  string
	Package string   `json:",omitempty"`
	Test    string   `json:",omitempty"`
	Elapsed *float64 `json:",omitempty"`
	Output  string   `json:",omitempty"`
}

func main() {
	os.Exit(Main())
}

func Main() int {
	err := mainErr()
	if err == nil {
		return 0
	}
	if errors.Is(err, errSilent) {
		return 1
	}
	fmt.Fprintf(os.Stderr, "gotestskip: %v\n", err)
	return 1
}

var errSilent = errors.New("silent error")

func mainErr() error {
	if len(os.Args) > 0 && os.Args[0] == "-help" || os.Args[0] == "-h" {
		usage()
		return nil
	}
	args := append([]string{"test", "-json"}, os.Args[1:]...)
	cmd := exec.Command("go", args...)
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	skipFile := os.Getenv("GO_SKIP_TESTS")
	if skipFile == "" {
		cmd.Stdout = os.Stdout
		if err := cmd.Run(); err != nil {
			if _, ok := err.(*exec.ExitError); ok {
				return errSilent
			}
			return err
		}
		return nil
	}
	f, err := os.ReadFile(skipFile)
	if err != nil {
		return err
	}
	var skip skipJSON
	if err := json.Unmarshal(f, &skip); err != nil {
		return fmt.Errorf("cannot unmarshal %q: %v", skipFile, err)
	}
	r, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(os.Stdout)
	ok := true
	for {
		var e event
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("error decoding go test output: %v", err)
		}
		if skip.Packages[e.Package][e.Test] {
			switch e.Action {
			case "fail":
				e.Action = "skip"
				wouldHaveFailed(e.Package, e.Test)
			case "output":
				e.Output = translateFailToSkip(e.Output)
			}
		} else if wouldHaveFailedMap[e.Package][e.Test] == suppressed && e.Action == "fail" {
			// The failure is (almost certainly) because a failed subtest has been skipped.
			e.Action = "success"
		} else if e.Action == "fail" {
			didFail(e.Package, e.Test)
		}
		if e.Action == "fail" {
			ok = false
		}
		if predictableOutput {
			makePredictable(&e)
		}
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	if !ok {
		return errSilent
	}
	return nil
}

type wouldFailStatus uint8

const (
	ok wouldFailStatus = iota
	failed
	suppressed
)

var wouldHaveFailedMap = make(map[string]map[string]wouldFailStatus)

// didFail marks the given test and all its parents as failing
// within the given package. This overrides any failure caused by
// suppression.
func didFail(pkg, test string) {
	setStatus(pkg, test, failed)
}

// didFail marks the given test and all its parents as suppressed (skipped)
// within the given package.
func wouldHaveFailed(pkg, test string) {
	setStatus(pkg, test, suppressed)
}

func setStatus(pkg, test string, status wouldFailStatus) {
	pm := wouldHaveFailedMap[pkg]
	if pm == nil {
		pm = make(map[string]wouldFailStatus)
		wouldHaveFailedMap[pkg] = pm
	}
	for _, t := range parents(test) {
		if tstatus := pm[t]; tstatus != failed {
			pm[t] = status
		}
	}
}

// parents returns test and all its parents, including the empty string for
// the package itself.
// For example parents("Foo/Bar/Baz") returns []string{"Foo/Bar/Baz", "Foo/Bar", "Foo", ""}
func parents(test string) []string {
	var p []string
	for {
		p = append(p, test)
		i := strings.LastIndex(test, "/")
		if i == -1 {
			return append(p, "")
		}
		test = test[:i]
	}
}

// translateFailToSkip translates a FAIL: output line
// to a SKIP: line, respecting indentation.
func translateFailToSkip(s string) string {
	i := 0
	for ; i < len(s); i++ {
		if s[i] != ' ' {
			break
		}
	}
	indent := s[:i]
	if s1 := strings.TrimLeft(s, "--- FAIL:"); len(s1) != len(s) {
		return indent + "--- SKIP:" + s1
	}
	return s
}

var testTimestampPat = regexp.MustCompile(`\(\d+\.\d+s\)\n`)
var packageTimestampPat = regexp.MustCompile(`\d+\.\d+s\n`)

// makePredictable makes event output predictable for tests by
// removing timestamps and changing test timings to zero.
func makePredictable(e *event) {
	e.Time = nil
	if e.Elapsed != nil {
		*e.Elapsed = 0
	}
	if e.Action != "output" {
		return
	}
	if e.Test != "" {
		e.Output = testTimestampPat.ReplaceAllString(e.Output, "(0.00s)\n")
	} else {
		e.Output = packageTimestampPat.ReplaceAllString(e.Output, "0.000s\n")
	}
}
