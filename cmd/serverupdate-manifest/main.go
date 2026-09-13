// serverupdate-manifest produces the server compatibility attachment that is
// subsequently bound into the existing canonical agent release manifest.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"p2pstream/internal/serverupdate"
)

func main() {
	version := flag.String("version", "", "exact release version")
	commit := flag.String("commit", "", "exact release commit")
	output := flag.String("output", "", "output attachment path")
	flag.Parse()
	m := serverupdate.NewMetadata(*version, *commit)
	if err := m.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *output == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "output path required, no positional arguments")
		os.Exit(1)
	}
	data, err := json.Marshal(m)
	if err == nil {
		err = os.WriteFile(*output, data, 0644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
