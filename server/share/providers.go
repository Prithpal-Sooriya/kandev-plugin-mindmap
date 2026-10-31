package share

import (
	"regexp"
	"strconv"

	"kandev-plugin-mindmap/server/contract"
)

// providerSpec is how one tunnel binary is launched and how its public URL
// is recognised in its combined stdout and stderr.
type providerSpec struct {
	bin     string
	install string
	args    func(port int) []string
	url     *regexp.Regexp
}

func specOf(p contract.ShareProvider) (providerSpec, bool) {
	s, ok := providerSpecs[p]
	return s, ok
}

// InstallCommand is the command the UI shows when the binary is missing.
func InstallCommand(p contract.ShareProvider) string {
	if s, ok := specOf(p); ok {
		return s.install
	}
	return ""
}

var providerSpecs = map[contract.ShareProvider]providerSpec{
	contract.ShareCloudflared: {
		bin:     "cloudflared",
		install: "brew install cloudflared",
		args: func(port int) []string {
			return []string{"tunnel", "--url", "http://127.0.0.1:" + strconv.Itoa(port)}
		},
		url: regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`),
	},
	contract.ShareTailscale: {
		bin:     "tailscale",
		install: "brew install tailscale",
		args: func(port int) []string {
			return []string{"funnel", strconv.Itoa(port)}
		},
		url: regexp.MustCompile(`https://[A-Za-z0-9.-]+\.ts\.net`),
	},
	contract.ShareNgrok: {
		bin:     "ngrok",
		install: "brew install ngrok",
		args: func(port int) []string {
			return []string{"http", "http://127.0.0.1:" + strconv.Itoa(port), "--log=stdout"}
		},
		url: regexp.MustCompile(`https://[A-Za-z0-9-]+\.ngrok[A-Za-z0-9.-]*`),
	},
}

// NotInstalledError is returned when the chosen tunnel binary cannot be found.
type NotInstalledError struct {
	Provider contract.ShareProvider
	Command  string
}

func (e *NotInstalledError) Error() string {
	return string(e.Provider) + " is not installed. Run: " + e.Command
}

// ContractError is the wire error. The message includes the install command
// so the UI can show it without a second field.
func (e *NotInstalledError) ContractError() *contract.Error {
	return &contract.Error{Code: contract.ErrNotInstalled, Message: e.Error()}
}
