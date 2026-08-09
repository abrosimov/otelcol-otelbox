package main

import (
	"bytes"
	"testing"
)

func TestRunRejectsIncompleteAndUnknownCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing command"},
		{name: "unknown command", args: []string{"manifest", "check"}},
		{name: "missing binary", args: []string{"binary", "check"}},
		{name: "unexpected binary argument", args: []string{"binary", "check", "extra"}},
		{name: "missing image", args: []string{"image", "smoke"}},
		{name: "unexpected image argument", args: []string{"image", "smoke", "extra"}},
		{name: "unexpected manifest argument", args: []string{"manifest", "resolve", "extra"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := run(test.args, &bytes.Buffer{}); err == nil {
				t.Fatal("run() error = nil, want an error")
			}
		})
	}
}
