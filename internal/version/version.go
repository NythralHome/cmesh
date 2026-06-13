package version

var (
	// Version is replaced by release builds.
	Version = "0.0.0-dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func String() string {
	return "cmesh " + Version + " (" + Commit + ", " + Date + ")"
}
