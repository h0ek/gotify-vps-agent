package platform

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type OSRelease map[string]string

func ReadOSRelease(path string) (OSRelease, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := OSRelease{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[key] = strings.Trim(strings.TrimSpace(value), "\"")
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func RequireDebian13() error {
	values, err := ReadOSRelease("/etc/os-release")
	if err != nil {
		return fmt.Errorf("read /etc/os-release: %w", err)
	}
	if values["ID"] != "debian" || values["VERSION_ID"] != "13" {
		return fmt.Errorf("Debian 13 is required; detected ID=%q VERSION_ID=%q", values["ID"], values["VERSION_ID"])
	}
	return nil
}
