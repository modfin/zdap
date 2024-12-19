package internal

import (
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/strslice"
	"github.com/modfin/henry/slicez"
)

type Resource struct {
	Name          string
	Alias         string
	Retrieval     string
	Creation      string
	Cron          string
	Docker        Docker
	ClonePool     ClonePoolConfig `yaml:"clone_pool"`
	RestoreParams ContainerParams `yaml:"restore_params"`
	CloneParams   ContainerParams `yaml:"clone_params"`
}

type Docker struct {
	Image       string
	Port        int
	Env         []string
	Entrypoint  []string
	Cmd         []string
	Volume      string
	Healthcheck string
	Shm         int64
}

const DefaultClaimTimeoutSeconds = 300
const DefaultClaimMaxTimeoutSeconds = 90000

type ClonePoolConfig struct {
	ResetOnNewSnap         bool `yaml:"reset_on_new_snap" json:"reset_on_new_snap"`
	MinClones              int  `yaml:"min_clones" json:"min_clones"`
	MaxClones              int  `yaml:"max_clones" json:"max_clones"`
	ClaimMaxTimeoutSeconds int  `yaml:"claim_max_timeout_seconds" json:"claim_max_timeout_seconds"`
	DefaultTimeoutSeconds  int  `yaml:"claim_default_timeout_seconds" json:"claim_default_timeout_seconds"`
}

type ContainerParams struct {
	Env           []string
	Entrypoint    []string
	Cmd           []string
	ZfsProperties []string `yaml:"zfs"`
}

func (r Resource) BaseCmd() strslice.StrSlice {
	if len(r.Docker.Cmd) == 0 && len(r.RestoreParams.Cmd) == 0 {
		return nil
	}
	return slicez.Concat(r.Docker.Cmd, r.RestoreParams.Cmd)
}

func (r Resource) BaseEnv() strslice.StrSlice {
	if len(r.Docker.Env) == 0 && len(r.RestoreParams.Env) == 0 {
		return nil
	}

	return slicez.Concat(r.Docker.Env, r.RestoreParams.Env)
}

func (r Resource) BaseEntrypoint() strslice.StrSlice {
	if len(r.Docker.Entrypoint) == 0 && len(r.RestoreParams.Entrypoint) == 0 {
		return nil
	}
	return slicez.Concat(r.Docker.Entrypoint, r.RestoreParams.Entrypoint)
}

func (r Resource) BaseZfsProperties() map[string]string {
	return r.zfsPropMap(r.RestoreParams.ZfsProperties)
}

func (r Resource) CloneCmd() strslice.StrSlice {
	if len(r.Docker.Cmd) == 0 && len(r.CloneParams.Cmd) == 0 {
		return nil
	}
	return slicez.Concat(r.Docker.Cmd, r.CloneParams.Cmd)
}

func (r Resource) CloneEnv() strslice.StrSlice {
	if len(r.Docker.Env) == 0 && len(r.CloneParams.Env) == 0 {
		return nil
	}

	return slicez.Concat(r.Docker.Env, r.CloneParams.Env)
}

func (r Resource) CloneEntrypoint() strslice.StrSlice {
	if len(r.Docker.Entrypoint) == 0 && len(r.CloneParams.Entrypoint) == 0 {
		return nil
	}
	return slicez.Concat(r.Docker.Entrypoint, r.CloneParams.Entrypoint)
}

func (r Resource) CloneZfsProperties() map[string]string {
	return r.zfsPropMap(r.CloneParams.ZfsProperties)
}

func (r Resource) zfsPropMap(config []string) map[string]string {
	if len(config) == 0 {
		return nil
	}
	zpm := make(map[string]string, len(config))
	for _, p := range config {
		kv := strings.Split(p, "=")
		if len(kv) != 2 {
			fmt.Printf("'%s' isn't a valid ZFS property/value combination (resource: %s)\n", p, r.Name)
			continue
		}
		zpm[kv[0]] = kv[1]
	}

	return zpm
}
