package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/personality"
)

const maxSuiteBytes = 8 << 20

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now))
}

func run(args []string, stdout, stderr io.Writer, now func() time.Time) int {
	flags := flag.NewFlagSet("yuri-personality-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inputPath := flags.String("input", "", "path to a yuri.personality-dogfood-suite JSON file")
	reportPath := flags.String("report", "", "optional path for the JSON report; stdout is always printed")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *inputPath == "" {
		fmt.Fprintln(stderr, "-input is required")
		return 2
	}

	suite, err := readSuite(*inputPath)
	if err != nil {
		fmt.Fprintf(stderr, "read suite: %v\n", err)
		return 2
	}
	report := personality.EvaluateDogfoodSuite(suite, now())
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "encode report: %v\n", err)
		return 2
	}
	encoded = append(encoded, '\n')
	if _, err := stdout.Write(encoded); err != nil {
		fmt.Fprintf(stderr, "write stdout: %v\n", err)
		return 2
	}
	if *reportPath != "" {
		if err := os.WriteFile(*reportPath, encoded, 0o600); err != nil {
			fmt.Fprintf(stderr, "write report: %v\n", err)
			return 2
		}
	}
	if !report.Passed {
		return 1
	}
	return 0
}

func readSuite(path string) (personality.DogfoodSuite, error) {
	file, err := os.Open(path)
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSuiteBytes+1))
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	if len(data) > maxSuiteBytes {
		return personality.DogfoodSuite{}, fmt.Errorf("suite exceeds %d bytes", maxSuiteBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var suite personality.DogfoodSuite
	if err := decoder.Decode(&suite); err != nil {
		return personality.DogfoodSuite{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return personality.DogfoodSuite{}, errors.New("suite contains multiple JSON values")
		}
		return personality.DogfoodSuite{}, err
	}
	return suite, nil
}
