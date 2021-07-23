package zdap

import (
	"fmt"
	"time"
)

type Clone struct {
	Resource  string
	Name      string
	Snap      string
	CreatedAt time.Time
	SnappedAt time.Time
	Port      int
	Address   string
}

//ListenPort    int    `env:"LISTEN_PORT"`
//TargetAddress string `env:"TARGET_ADDRESS"`

func (c Clone) YamlOverride(listenPort int) string {
	return fmt.Sprintf(`
  %s:
    image: crholm/zdap-proxy:latest
    environment:
      - LISTEN_PORT=%d
      - TARGET_ADDRESS=%s:%d
    ports:
      - "%d:%d"
`, c.Resource, listenPort, c.Address, c.Port, listenPort, listenPort)
}

type PublicResource struct {
	Name  string       `json:"name"`
	Snaps []PublicSnap `json:"snaps"`
}
type PublicSnap struct {
	CreatedAt time.Time     `json:"created_at"`
	Clones    []PublicClone `json:"clones"`
}
type PublicClone struct {
	CreatedAt time.Time `json:"created_at"`
}
