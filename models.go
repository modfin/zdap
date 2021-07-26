package zdap

import (
	"fmt"
	"time"
)

type PublicResource struct {
	Name   string       `json:"name"`
	Alias  string       `json:"alias"`
	Snaps  []PublicSnap `json:"snaps"`
}
type PublicSnap struct {
	Name      string        `json:"name"`
	Resource  string        `json:"resource"`
	CreatedAt time.Time     `json:"created_at"`
	Clones    []PublicClone `json:"clones"`
}
type PublicClone struct {
	Name      string    `json:"name"`
	Resource  string    `json:"resource"`
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"created_at"`
	SnappedAt time.Time `json:"snapped_at"`
	Server    string 	`json:"server"`
	Port      int       `json:"port"`
}

func (c *PublicClone) YAML(listenPort int) string {
	return fmt.Sprintf(`
  %s:
    image: crholm/zdap-proxy:latest
    environment:
      - LISTEN_PORT=%d
      - TARGET_ADDRESS=%s:%d
    ports:
      - "%d:%d"
`, c.Resource, listenPort, c.Server, c.Port, listenPort, listenPort)
}
