package version

// Version is set at build time via ldflags:
// go build -ldflags "-X github.com/wesm/roborev/internal/version.Version=$(git rev-parse --short HEAD)"
var Version = "dev"
