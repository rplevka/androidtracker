package buildinfo

import "runtime/debug"

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

type Info struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	ShortCommit string `json:"short_commit"`
	BuildTime   string `json:"build_time,omitempty"`
}

func Current() Info {
	return Info{
		Version:     Version,
		Commit:      Commit,
		ShortCommit: shortCommit(Commit),
		BuildTime:   BuildTime,
	}
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if Version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		Version = info.Main.Version
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if Commit == "unknown" && setting.Value != "" {
				Commit = setting.Value
			}
		case "vcs.time":
			if BuildTime == "unknown" && setting.Value != "" {
				BuildTime = setting.Value
			}
		}
	}
}
