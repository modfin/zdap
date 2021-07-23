package zdap

import "time"

type Clone struct {
	Resource string
	Name string
	Snap string
	CreatedAt time.Time
	SnappedAt time.Time
	Port int
	Address string
}
