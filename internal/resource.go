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
