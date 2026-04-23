// Package version содержит build-информацию, проставляемую через -ldflags
// во время сборки (см. .goreleaser.yaml). В дев-сборке значения имеют префикс
// "dev-" — это нормально, продовый релиз обязательно заменит их на реальные.
package version

import (
	"fmt"
	"runtime"
)

var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

func (i Info) String() string {
	return fmt.Sprintf("log_analyser %s (commit=%s, built=%s, %s/%s, %s)",
		i.Version, i.Commit, i.BuildDate, i.OS, i.Arch, i.GoVersion)
}
