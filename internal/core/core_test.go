package core

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func Test_loadResources(t *testing.T) {
	rs, err := loadResources("./testdata/resources")
	assert.NoError(t, err)
	assert.Len(t, rs, 1)
	assert.Equal(t, "postgres-trade", rs[0].Name)
	assert.Equal(t, 8, rs[0].ClonePool.MinClones)
	assert.Equal(t, 16, rs[0].ClonePool.MaxClones)
}
