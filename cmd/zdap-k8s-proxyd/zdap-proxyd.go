package main

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal/utils"
)

func attachNewClone() *zdap.PublicClone {
	var cli *zdap.Client
	var cliScore float64

	zdapServerScore := func(stat *zdap.ServerStatus) float64 { // higher the better
		disk := stat.FreeDisk
		clones := stat.Clones
		mem := stat.FreeMem
		load := stat.Load15

		sum := math.Log2(float64(disk) / float64(datasize.GB) / 100.0) // more disk is good
		sum += math.Log2(float64(mem) / float64(datasize.GB))          // more ram is good
		if clones > 0 {
			sum -= math.Log2(float64(clones)) // fewer clones is good
		}
		if load > 0 {
			sum -= math.Log2(load) // load less than 1 is good
		}
		return sum
	}

	for _, s := range PodCfg.Servers {
		if !strings.Contains(s, ":") {
			s = fmt.Sprintf("%s:%d", s, PodCfg.APIPort)
		}

		c := zdap.NewClient(PodCfg.CloneOwnerName, s)
		stat, err := c.Status()
		if err != nil {
			fmt.Printf("error connecting to server: %v\n", err)
			continue
		}
		if !utils.StringSliceContains(stat.Resources, PodCfg.Resource) {
			continue
		}
		cs := zdapServerScore(stat)
		if cs > cliScore {
			cli = c
			cliScore = cs
		}
	}
	if cli == nil {
		log.Fatalf("ERROR: no zdap server with '%s' resource could be found!\n", PodCfg.Resource)
	}

	clone, err := cli.CloneSnap(PodCfg.Resource, time.Time{}, zdap.ClaimArgs{})
	if err != nil {
		log.Fatalf("ERROR: failed to clone '%s' resource, error %v\n", PodCfg.Resource, err)
	}

	clone.Server = cli.Server()
	return clone
}

func main() {
	clone := attachNewClone()

	p := newProxy(PodCfg.ListenPort, clone.Server, clone.Port)
	p.Run()
}
