package updates

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"strings"
)

const DefaultStateFile = "/etc/contabase/install-state.env"

type Identity struct {
	Version string
	Channel string
	Method  string
}

var releaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta\.([1-9][0-9]*))?$`)

// The persisted installation channel is authoritative; the tag alone cannot
// distinguish a channel installation from an intentionally pinned version.
func ReadIdentity(runtimeVersion, stateFile string) Identity {
	id := Identity{Version: runtimeVersion}
	if stateFile == "" {
		stateFile = DefaultStateFile
	}
	f, err := os.Open(stateFile)
	if err != nil {
		return id
	}
	defer f.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(f, 32769))
	size := 0
	for scanner.Scan() {
		size += len(scanner.Bytes()) + 1
		if size > 32768 {
			return id
		}
		key, value, ok := strings.Cut(scanner.Text(), "=")
		switch key {
		case "CONTABASE_STATE_VERSION", "CONTABASE_CHANNEL", "CONTABASE_INSTALLED_VERSION", "CONTABASE_INSTALL_METHOD":
			if _, exists := values[key]; exists || !ok {
				return id
			}
			values[key] = value
		}
	}
	if scanner.Err() != nil || values["CONTABASE_STATE_VERSION"] != "1" || values["CONTABASE_INSTALLED_VERSION"] != runtimeVersion {
		return id
	}
	switch values["CONTABASE_INSTALL_METHOD"] {
	case "docker", "docker-image", "release", "source", "source-bootstrap":
	default:
		return id
	}
	channel := values["CONTABASE_CHANNEL"]
	if versionChannel(runtimeVersion) != channel || channel == "" {
		return id
	}
	id.Channel, id.Method = channel, values["CONTABASE_INSTALL_METHOD"]
	return id
}

func versionChannel(v string) string {
	if !releaseVersion.MatchString(v) {
		return ""
	}
	if strings.Contains(v, "-beta.") {
		return "beta"
	}
	return "stable"
}

// Numeric components are compared as decimal strings to avoid integer overflow.
func compareVersion(a, b string) int {
	x, y := releaseVersion.FindStringSubmatch(a), releaseVersion.FindStringSubmatch(b)
	for _, i := range []int{1, 2, 3} {
		if n := compareNumber(x[i], y[i]); n != 0 {
			return n
		}
	}
	if x[4] == "" && y[4] != "" {
		return 1
	}
	if x[4] != "" && y[4] == "" {
		return -1
	}
	return compareNumber(x[5], y[5])
}

func compareNumber(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
