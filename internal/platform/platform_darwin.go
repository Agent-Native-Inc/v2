package platform

const (
	// ReleaseFile is this platform's download in a release. Old v2s fetch this name too.
	ReleaseFile = "v2-macos.zip"
	// Opener opens a URL in the browser.
	Opener = "open"
)

var installs = map[string]string{"git": "xcode-select --install", "ffmpeg": "brew install ffmpeg"}

// InstallHint tells the user how to install pkg, as a sentence.
func InstallHint(pkg string) string {
	return "Install " + pkg + " with `" + installs[pkg] + "`"
}
