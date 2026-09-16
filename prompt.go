package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
)

// errNotInteractive means there was no terminal to put a question to — a cron
// run, typically. Callers turn it into an error that says what they would have
// asked and how to answer it ahead of time instead.
var errNotInteractive = errors.New("not running interactively")

// promptReader buffers stdin across questions. A fresh bufio.Reader per
// question would discard whatever it had already pulled into its buffer.
var promptReader *bufio.Reader

// isInteractive reports whether questions can be asked. Replaced in tests.
var isInteractive = func() bool {
	return isatty.IsTerminal(os.Stdin.Fd())
}

// choice is one answer to a multiple-choice question. Key is the letter typed
// to pick it.
type choice struct {
	Key   string
	Label string
}

func readAnswer() (string, error) {
	if promptReader == nil {
		promptReader = bufio.NewReader(os.Stdin)
	}

	line, err := promptReader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if err != nil && answer == "" {
		return "", err
	}

	return answer, nil
}

// askYesNo puts question to the user, returning def when they just hit enter.
func askYesNo(question string, def bool) (bool, error) {
	if !isInteractive() {
		return false, errNotInteractive
	}

	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}

	for {
		fmt.Printf("%s %s ", question, hint)

		answer, err := readAnswer()
		if err != nil {
			// Stdin closed mid-question: no way to get an answer, same as
			// never having had a terminal.
			fmt.Println()
			return false, errNotInteractive
		}

		switch answer {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}

		fmt.Println("Please answer y or n.")
	}
}

// askChoice puts question to the user along with the available answers,
// returning the Key of the one they picked. def is the Key used when they just
// hit enter.
func askChoice(question string, choices []choice, def string) (string, error) {
	if !isInteractive() {
		return "", errNotInteractive
	}

	keys := make([]string, 0, len(choices))
	for _, c := range choices {
		key := c.Key
		if key == def {
			key = strings.ToUpper(key)
		}
		keys = append(keys, key)
	}
	hint := fmt.Sprintf("[%s]", strings.Join(keys, "/"))

	for {
		fmt.Println(question)
		for _, c := range choices {
			fmt.Printf("  %s) %s\n", c.Key, c.Label)
		}
		fmt.Printf("Choice %s ", hint)

		answer, err := readAnswer()
		if err != nil {
			fmt.Println()
			return "", errNotInteractive
		}

		if answer == "" {
			answer = def
		}

		for _, c := range choices {
			if answer == c.Key {
				return c.Key, nil
			}
		}

		fmt.Printf("Please answer with one of %s.\n", strings.Join(keys, ", "))
	}
}

// confirmMissingDir asks whether to create a missing directory. kind and
// display name it for the user, e.g. "Remote destination" and "host:/backups".
//
// It returns true when the caller should go ahead and create the directory,
// false when a dry run means nothing should be created, and an error when the
// run cannot continue without the directory.
func confirmMissingDir(kind, display string) (bool, error) {
	if dryRun {
		fmt.Printf("[DRY-RUN] %s %s does not exist, would offer to create it\n", kind, display)
		return false, nil
	}

	create, err := askYesNo(fmt.Sprintf("%s %s does not exist. Create it?", kind, display), true)
	if errors.Is(err, errNotInteractive) {
		return false, fmt.Errorf("%s %s does not exist: create it, or run btrfs-backup from a terminal to be asked", strings.ToLower(kind), display)
	}
	if err != nil {
		return false, err
	}
	if !create {
		return false, fmt.Errorf("%s %s does not exist", strings.ToLower(kind), display)
	}

	return true, nil
}
