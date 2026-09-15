package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ingot-agent/ingot/internal/buildinfo"
	ingotrelease "github.com/ingot-agent/ingot/internal/release"
)

func main() {
	version := flag.String("version", "", "release version or tag")
	commit := flag.String("commit", "", "release commit")
	sourceDate := flag.Int64("source-date", 0, "source commit Unix timestamp")
	input := flag.String("input", "", "directory containing platform binaries")
	output := flag.String("output", "", "release asset directory")
	license := flag.String("license", "LICENSE", "license file")
	installSH := flag.String("install-sh", "scripts/install.sh", "Unix installer")
	installPS1 := flag.String("install-ps1", "scripts/install.ps1", "PowerShell installer")
	printDevelopmentVersion := flag.Bool("print-development-version", false, "print the source-tree core version")
	flag.Parse()
	if *printDevelopmentVersion {
		_, _ = fmt.Fprintln(os.Stdout, buildinfo.CoreVersion)
		return
	}
	if flag.NArg() != 0 || *version == "" || *commit == "" || *sourceDate <= 0 || *input == "" || *output == "" {
		flag.Usage()
		os.Exit(2)
	}
	manifest, err := ingotrelease.PackCoreRelease(ingotrelease.PackOptions{
		Version: *version, Commit: *commit, SourceTime: time.Unix(*sourceDate, 0).UTC(),
		InputDirectory: *input, OutputDirectory: *output, LicensePath: *license,
		InstallSHPath: *installSH, InstallPS1Path: *installPS1,
	})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(os.Stdout, manifest.Tag)
}
