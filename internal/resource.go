package internal


type Resource struct {
	Name      string
	Retrieval string
	Creation string
	Cron string
	Docker Docker
}

type Docker struct {
	Image string
	Port int
	Env []string
	Volume string
	Healthcheck string
}
