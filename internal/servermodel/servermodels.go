package servermodel

import (
	"github.com/kraudcloud/go-libzfs/v2"
	"github.com/modfin/zdap"
)

type ServerInternalResource struct {
	zdap.PublicResource
	Snaps []ServerInternalSnapshot `json:"snaps"`
}
type ServerInternalSnapshot struct {
	zdap.PublicSnap
	Clones []ServerInternalClone `json:"clones"`
}
type ServerInternalClone struct {
	zdap.PublicClone
	Dataset *zfs.Dataset
}
