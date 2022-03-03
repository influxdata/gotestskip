package main

import (
	"os"
	"testing"

	"github.com/rogpeppe/go-internal/gotooltest"
	"github.com/rogpeppe/go-internal/testscript"
)

func TestScript(t *testing.T) {
	p := testscript.Params{
		Dir: "testdata",
	}
	if err := gotooltest.Setup(&p); err != nil {
		t.Fatal(err)
	}
	testscript.Run(t, p)
}

func TestMain(m *testing.M) {
	omitTime = true
	os.Exit(testscript.RunMain(m, map[string]func() int{
		"gotestskip": Main,
	}))
}
