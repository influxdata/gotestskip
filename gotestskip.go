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

func usage() {
	fmt.Fprintf(os.Stderr, `
usage: gotestskip [go test args...]

If $GO_SKIP_TESTS is empty or unset, this command functions
like "go test -json ..."; otherwise is tries to read the file named
by $GO_SKIP_TESTS, which should contain a file containing
YAML in the following
format:

	somerepo.com/pkg0:
	-TestX/subtest
	-TestX/othertest
	-TestY
	somerepo.com/pkg1:
	-TestZ 2022-03-04

The top level key specifies a package in which to skip tests. Each
value inside that specifies a test to skip. Skipping a test will skip all its subtests too.

If $GO_SKIP_ROOT is set, it's used as the prefix for package names.
So if GO_SKIP_ROOT is set to "somerepo.com", the
above configuration can be specified as:

	pkg0:
	-TestX/subtest
	-TestX/othertest
	-TestY
	pkg1:
	-TestZ 2022-03-04

Note that even when a test is marked to be skipped, it will still actually
run - just any failure will cause the test to be marked as skipped rather
than failing. This means that gotestskip is ineffectual at skipping tests
that run for a very long time, or cause a panic, for example.

The name of a test may be followed by a date in YYYY-MM-DD format,
which can be used to specify when the test was skipped. This is checked
for syntax but otherwise ignored by this command.

It can be run under gotestsum as:

	gotestsum --raw-command -- gotestskip $go_cmd_args
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
	skip, err := readSkipConfig(skipFile, os.Getenv("GO_SKIP_ROOT"))
	if err != nil {
		return err
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
		switch {
		case shouldSkip(skip, e.Package, e.Test):
			// The test is marked to be skipped.
			switch e.Action {
			case "fail":
				e.Action = "skip"
				wouldHaveFailed(e.Package, e.Test)
			case "output":
				translateFailOutput(&e, "SKIP")
			case "run":
				// The FAIL output for a parent test is produced
				// before any of the FAIL (or Action: "fail") events
				// of its subtests, so at that point we can't know
				// whether to output FAIL or PASS.
				// We could theoretically buffer output until the
				// status of all subtests is known, but that's a bunch
				// of complexity we don't want right now, so instead
				// mark this test and its parents as skipping, so
				// that we can delay the parent output until its status
				// is known.
				setSkipping(e.Package, e.Test)
			}
		case getStatus(e.Package, e.Test) == statusSuppressed:
			// A subtest has failed but was skipped.
			// If the test has failed, it's almost certainly because of this.
			switch e.Action {
			case "fail":
				if e.Test == "" {
					// Package-level status is marked as "success" not "pass".
					e.Action = "success"
				} else {
					e.Action = "pass"
				}
			case "output":
				translateFailOutput(&e, "PASS")
			}
			if err := sendSavedEvents(enc, e.Package, e.Test, "PASS"); err != nil {
				return err
			}
		case e.Action == "fail":
			didFail(e.Package, e.Test)
			if err := sendSavedEvents(enc, e.Package, e.Test, ""); err != nil {
				return err
			}
		case getStatus(e.Package, e.Test) == statusSkipping && e.Action == "output":
			// Buffer output of a parent test until we know the status of its subtests.
			saveEvent(e.Package, e.Test, e)
			continue

		case e.Action == "pass":
			if err := sendSavedEvents(enc, e.Package, e.Test, ""); err != nil {
				return err
			}
		}
		if e.Action == "fail" {
			ok = false
		}
		if err := sendEvent(enc, &e); err != nil {
			return err
		}
	}
	if !ok {
		return errSilent
	}
	return nil
}

type testStatus uint8

const (
	statusUnknown testStatus = iota
	statusSkipping
	statusFailed
	statusSuppressed
)

var savedEvents = make(map[string]map[string][]event)

func saveEvent(pkg, test string, e event) {
	pm := savedEvents[pkg]
	if pm == nil {
		pm = make(map[string][]event)
		savedEvents[pkg] = pm
	}
	pm[test] = append(pm[test], e)
}

func sendSavedEvents(enc *json.Encoder, pkg, test string, status string) error {
	pm := savedEvents[pkg]
	if pm == nil {
		return nil
	}
	saved, ok := pm[test]
	if ok {
		delete(pm, test)
	}
	for i := range saved {
		e := &saved[i]
		if status != "" {
			translateFailOutput(e, "PASS")
		}
		if err := sendEvent(enc, e); err != nil {
			return err
		}
	}
	return nil
}

func sendEvent(enc *json.Encoder, e *event) error {
	if predictableOutput {
		makePredictable(e)
	}
	return enc.Encode(e)
}

var testStatusMap = make(map[string]map[string]testStatus)

func getStatus(pkg, test string) testStatus {
	return testStatusMap[pkg][test]
}

// shouldSkip reports whether a given test should
// be skipped. A test is skipped if it or any of its parents
// are marked to be skipped.
func shouldSkip(skip *skipConfig, pkg, test string) bool {
	pm := skip.Packages[pkg]
	if pm == nil {
		return false
	}
	for _, t := range parents(test) {
		if pm[t] {
			return true
		}
	}
	return false
}

// didFail marks the given test and all its parents as failing
// within the given package. This overrides any failure caused by
// suppression.
func didFail(pkg, test string) {
	setStatus(pkg, test, func(status testStatus) testStatus {
		return statusFailed
	})
}

// didFail marks the given test and all its parents as suppressed (skipped)
// within the given package.
func wouldHaveFailed(pkg, test string) {
	setStatus(pkg, test, func(status testStatus) testStatus {
		if status != statusFailed {
			status = statusSuppressed
		}
		return status
	})
}

func setSkipping(pkg, test string) {
	setStatus(pkg, test, func(status testStatus) testStatus {
		if status == statusUnknown {
			status = statusSkipping
		}
		return status
	})
}

func setStatus(pkg, test string, getStatus func(oldStatus testStatus) testStatus) {
	pm := testStatusMap[pkg]
	if pm == nil {
		pm = make(map[string]testStatus)
		testStatusMap[pkg] = pm
	}
	for _, t := range parents(test) {
		oldStatus := pm[t]
		if status := getStatus(oldStatus); status != oldStatus {
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

// translateFailOutput translates a FAIL output line
// to a SKIP or PASS line (specified by to), respecting indentation.
func translateFailOutput(e *event, to string) {
	if e.Test == "" {
		// It's output at the package level. As the top level can never
		// be skipped, we ignore to.
		if e.Output == "FAIL\n" {
			e.Output = "PASS\n"
			return
		}
		if s := strings.TrimPrefix(e.Output, "FAIL\t"); len(s) != len(e.Output) {
			// e.g. "FAIL\tpkg\t0.000s\n" -> "ok  \tpkg\t0.000s\n"
			e.Output = "ok  \t" + s
			return
		}
		return
	}
	s := e.Output
	i := 0
	for ; i < len(s); i++ {
		if s[i] != ' ' {
			break
		}
	}
	indent := s[:i]
	if s1 := strings.TrimPrefix(s[i:], "--- FAIL: "); len(s1) != len(s[i:]) {
		e.Output = indent + "--- " + to + ": " + s1
	}
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
