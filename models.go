package zdap

import (
	"fmt"
	"time"
)

type PublicResource struct {
	Name  string       `json:"name"`
	Alias string       `json:"alias"`
	Snaps []PublicSnap `json:"snaps"`
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
	Server    string    `json:"server"`
	APIPort   int       `json:"api_port"`
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

type ServerStatus struct {
	Address   string   `json:"address"`
	Resources []string `json:"resources"`
	Snaps     int      `json:"snaps"`
	Clones    int      `json:"clones"`
	FreeDisk  uint64   `json:"free_disk"`
	UsedDisk  uint64   `json:"used_disk"`
	TotalDisk uint64   `json:"total_disk"`
	Load1     float64  `json:"load_1"`
	Load5     float64  `json:"load_5"`
	Load15    float64  `json:"load_15"`
	FreeMem   uint64   `json:"free_mem"`
	CachedMem uint64   `json:"cached_mem"`
	TotalMem  uint64   `json:"total_mem"`
	UsedMem   uint64   `json:"used_mem"`
}
