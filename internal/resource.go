package internal

type Resource struct {
	Name      string
	Alias     string
	Retrieval string
	Creation  string
	Cron      string
	Docker    Docker
	ClonePool ClonePoolConfig `yaml:"clone_pool"`
}

type Docker struct {
	Image       string
	Port        int
	Env         []string
	Volume      string
	Healthcheck string
}

const DefaultClaimMaxTimeoutSeconds = 300

type ClonePoolConfig struct {
	ResetOnNewSnap         bool `yaml:"reset_on_new_snap"`
	MinClones              int  `yaml:"min_clones"`
	MaxClones              int  `yaml:"max_clones"`
	ClaimMaxTimeoutSeconds int  `yaml:"claim_max_timeout_seconds"`
}
