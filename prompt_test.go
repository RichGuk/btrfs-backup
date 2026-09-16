package main

import (
	"errors"
	"strings"
	"testing"
)

func TestAskYesNo(t *testing.T) {
	cases := []struct {
		name    string
		answers []string
		def     bool
		want    bool
	}{
		{name: "yes", answers: []string{"y"}, def: false, want: true},
		{name: "spelled out yes", answers: []string{"YES"}, def: false, want: true},
		{name: "no", answers: []string{"n"}, def: true, want: false},
		{name: "empty takes the default", answers: []string{""}, def: true, want: true},
		{name: "retries until valid", answers: []string{"maybe", "n"}, def: true, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withPromptAnswers(t, tc.answers...)

			var (
				got bool
				err error
			)
			captureStdout(t, func() { got, err = askYesNo("Create it?", tc.def) })
			if err != nil {
				t.Fatalf("askYesNo: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestAskYesNoWithoutTerminal(t *testing.T) {
	withoutTerminal(t)

	if _, err := askYesNo("Create it?", true); !errors.Is(err, errNotInteractive) {
		t.Fatalf("expected errNotInteractive, got %v", err)
	}
}

// Stdin running out mid-question is the same problem as never having had a
// terminal: there is no answer coming.
func TestAskYesNoTreatsEOFAsNoTerminal(t *testing.T) {
	withPromptAnswers(t)

	var err error
	captureStdout(t, func() { _, err = askYesNo("Create it?", true) })
	if !errors.Is(err, errNotInteractive) {
		t.Fatalf("expected errNotInteractive, got %v", err)
	}
}

func TestAskChoice(t *testing.T) {
	choices := []choice{
		{Key: "s", Label: "btrfs subvolume"},
		{Key: "d", Label: "plain directory"},
		{Key: "n", Label: "do not create it"},
	}

	cases := []struct {
		name    string
		answers []string
		want    string
	}{
		{name: "picks an option", answers: []string{"d"}, want: "d"},
		{name: "empty takes the default", answers: []string{""}, want: "s"},
		{name: "retries until valid", answers: []string{"x", "n"}, want: "n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withPromptAnswers(t, tc.answers...)

			var (
				got string
				err error
			)
			captureStdout(t, func() { got, err = askChoice("Create it as:", choices, "s") })
			if err != nil {
				t.Fatalf("askChoice: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

// The default is the one shown in upper case, so it is obvious what enter does.
func TestAskChoiceMarksTheDefault(t *testing.T) {
	withPromptAnswers(t, "")

	output := captureStdout(t, func() {
		if _, err := askChoice("Create it as:", []choice{{Key: "s", Label: "subvolume"}, {Key: "d", Label: "directory"}}, "s"); err != nil {
			t.Fatalf("askChoice: %v", err)
		}
	})

	if !strings.Contains(output, "[S/d]") {
		t.Fatalf("expected the default to be marked in the hint, got %q", output)
	}
}

func TestAskChoiceWithoutTerminal(t *testing.T) {
	withoutTerminal(t)

	_, err := askChoice("Create it as:", []choice{{Key: "s", Label: "subvolume"}}, "s")
	if !errors.Is(err, errNotInteractive) {
		t.Fatalf("expected errNotInteractive, got %v", err)
	}
}
