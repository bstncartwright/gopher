package update

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var semverPattern = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

type parsedVersion struct {
	major      int
	minor      int
	patch      int
	prerelease string
}

func CompareVersions(current, latest string) (int, error) {
	currentVersion, err := parseVersion(current)
	if err != nil {
		return 0, fmt.Errorf("parse current version: %w", err)
	}
	latestVersion, err := parseVersion(latest)
	if err != nil {
		return 0, fmt.Errorf("parse latest version: %w", err)
	}

	if currentVersion.major != latestVersion.major {
		return compareInt(currentVersion.major, latestVersion.major), nil
	}
	if currentVersion.minor != latestVersion.minor {
		return compareInt(currentVersion.minor, latestVersion.minor), nil
	}
	if currentVersion.patch != latestVersion.patch {
		return compareInt(currentVersion.patch, latestVersion.patch), nil
	}
	return comparePrerelease(currentVersion.prerelease, latestVersion.prerelease), nil
}

func parseVersion(raw string) (parsedVersion, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return parsedVersion{}, fmt.Errorf("version is required")
	}
	matches := semverPattern.FindStringSubmatch(value)
	if len(matches) == 0 {
		return parsedVersion{}, fmt.Errorf("unsupported version format %q", raw)
	}

	major, _ := strconv.Atoi(matches[1])
	minor, _ := strconv.Atoi(matches[2])
	patch, _ := strconv.Atoi(matches[3])
	return parsedVersion{
		major:      major,
		minor:      minor,
		patch:      patch,
		prerelease: matches[4],
	}, nil
}

func compareInt(left, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func comparePrerelease(current, latest string) int {
	current = strings.TrimSpace(current)
	latest = strings.TrimSpace(latest)
	switch {
	case current == latest:
		return 0
	case current == "":
		return 1
	case latest == "":
		return -1
	}

	currentParts := strings.Split(current, ".")
	latestParts := strings.Split(latest, ".")
	for i := 0; i < len(currentParts) && i < len(latestParts); i++ {
		currentPart := currentParts[i]
		latestPart := latestParts[i]
		if currentPart == latestPart {
			continue
		}

		currentNum, currentIsNum := numericIdentifier(currentPart)
		latestNum, latestIsNum := numericIdentifier(latestPart)
		switch {
		case currentIsNum && latestIsNum:
			return compareInt(currentNum, latestNum)
		case currentIsNum && !latestIsNum:
			return -1
		case !currentIsNum && latestIsNum:
			return 1
		case currentPart < latestPart:
			return -1
		default:
			return 1
		}
	}

	return compareInt(len(currentParts), len(latestParts))
}

func numericIdentifier(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return number, true
}
