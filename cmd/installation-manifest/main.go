package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"p2pstream/internal/serverupdate"
)

func main() {
	version := flag.String("version", "", "release version")
	commit := flag.String("commit", "", "release commit")
	channel := flag.String("channel", "", "release channel")
	repository := flag.String("repository", "", "publisher repository")
	amd64 := flag.String("amd64", "", "updater amd64 platform digest")
	arm64 := flag.String("arm64", "", "updater arm64 platform digest")
	output := flag.String("output", "", "descriptor output")
	flag.Parse()
	descriptor := serverupdate.Installation{API: 1, Version: *version, Commit: *commit, Channel: *channel, Bundle: "p2pstream_" + *version + "_docker.tar.gz", UpdaterImages: map[string]string{"linux/amd64": *amd64, "linux/arm64": *arm64}}
	err := descriptor.Validate(*repository)
	if err == nil && (*output == "" || flag.NArg() != 0) {
		err = fmt.Errorf("output required, no positional arguments")
	}
	if err == nil {
		var data []byte
		data, err = json.Marshal(descriptor)
		if err == nil {
			err = os.WriteFile(*output, data, 0644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
