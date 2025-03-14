package compose

import (
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

// Add adds a container to the compose file, will err if file does not exist
func Add(dest string, resource string, container *Container) error {
	// Get exclusive lock on file first to prevent race conditions
	f, err := os.OpenFile(dest, os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Use flock to get an exclusive lock
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("failed to acquire exclusive lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // Release lock when done

	// Read the file after obtaining the lock
	d, err := io.ReadAll(f)
	if err != nil {
		return err
	}

	var comp DockerCompose
	err = yaml.Unmarshal(d, &comp)
	if err != nil {
		return err
	}

	if comp.Services == nil {
		comp.Services = map[string]*Container{}
	}

	comp.Services[resource] = container

	// Clear all build nodes, since those will be (re-)written after we have marshaled the data
	for _, overrideContainer := range comp.Services {
		overrideContainer.Build = nil
	}

	data, err := yaml.Marshal(comp)
	if err != nil {
		return err
	}

	// Fix issue with zdap-proxy image being overwritten by dcr/docker compose builds, by inserting a '!reset' YAML
	// custom tag, on the 'build' node. The !reset custom tag causes Docker to clear the build node when it's merging
	// the compose files. gopkg.in/yaml.v3 support parsing YAML with custom tags, but I could not find an easy way to
	// add them when marshaling the changes to the override file, so reverted to just string manipulation of the
	// marshalled data.
	//
	// Insert 'build: !reset {}' before each 'image:' found in the override YAML
	overrideDataStr := string(data)
	var ofs, pos int
	for {
		pos = strings.Index(overrideDataStr[ofs:], "image:")
		if pos == -1 {
			break
		}
		old := overrideDataStr
		overrideDataStr = old[:pos+ofs] + "build: !reset {}\n        " + old[pos+ofs:]
		ofs += pos + 30
	}

	// Truncate the file before writing to it
	err = f.Truncate(0)
	if err != nil {
		return err
	}

	// Reset file pointer to beginning
	_, err = f.Seek(0, 0)
	if err != nil {
		return err
	}

	_, err = f.Write([]byte(overrideDataStr))
	return err
}

func RemoveClone(dest string, resources []string) error {
	// Get exclusive lock on file first to prevent race conditions
	f, err := os.OpenFile(dest, os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Use flock to get an exclusive lock
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("failed to acquire exclusive lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // Release lock when done

	// Read the file after obtaining the lock
	d, err := io.ReadAll(f)
	if err != nil {
		return err
	}

	var comp DockerCompose
	err = yaml.Unmarshal(d, &comp)
	if err != nil {
		return err
	}

	for _, resource := range resources {
		delete(comp.Services, resource)
	}

	data, err := yaml.Marshal(comp)
	if err != nil {
		return err
	}

	// Truncate the file before writing to it
	err = f.Truncate(0)
	if err != nil {
		return err
	}

	// Reset file pointer to beginning
	_, err = f.Seek(0, 0)
	if err != nil {
		return err
	}

	_, err = f.Write(data)
	return err
}
