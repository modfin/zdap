package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/modfin/henry/slicez"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal/utils"
)

type k8sp struct {
	proxy *TCPProxy
	clone *zdap.PublicClone
}

func useK8sProxy() bool {
	cfg := Config()
	return cfg.CloneOwnerName != "" || cfg.ResetAtHhMm != "" || cfg.Resource != "" || len(cfg.Servers) > 0
}

func k8sProxy() *k8sp {
	cfg := Config()
	if cfg.CloneOwnerName == "" {
		log.Fatal("ERROR: ZDAP_CLONE_OWNER_NAME environment variable must be set, and should be unique on the zdap server\n")
	}
	if cfg.Resource == "" {
		log.Fatal("ERROR: ZDAP_RESOURCE environment variable must be set to a valid resource name\n")
	}
	if len(cfg.Servers) == 0 {
		log.Fatal("ERROR: ZDAP_SERVERS environment variable must be set\n")
	}

	return &k8sp{}
}

func (p *k8sp) Start(ctx context.Context) {
	cfg := Config()

	log.Printf("Checking for an existing %s clone...\n", cfg.Resource)
	p.clone = p.getExistingClone()
	if p.clone == nil {
		log.Printf("Trying to create a new %s clone...\n", cfg.Resource)
		p.clone = p.attachNewClone()
	}

	p.proxy = &TCPProxy{
		ListenPort:    cfg.ListenPort,
		TargetAddress: fmt.Sprintf("%s:%d", p.clone.Server, p.clone.Port),
	}
	p.proxy.Start(ctx)

	if cfg.ResetAtHhMm != "" {
		p.setupResetTimer(ctx, cfg.ResetAtHhMm)
	}
}

func (p *k8sp) Stop() {
	if p.proxy != nil {
		p.proxy.Stop()
	}
}

func (p *k8sp) attachNewClone() *zdap.PublicClone {
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

	cfg := Config()
	var wg sync.WaitGroup
	var mu sync.Mutex
	type availableSnap struct {
		zdap.PublicSnap
		cli   *zdap.Client
		score float64
	}
	var availableSnaps []availableSnap
	for _, s := range cfg.Servers {
		wg.Add(1)
		go func(server string) {
			defer wg.Done()
			if !strings.Contains(server, ":") {
				server = fmt.Sprintf("%s:%d", server, cfg.APIPort)
			}

			cli := zdap.NewClient(http.DefaultClient, cfg.CloneOwnerName, server)
			stat, err := cli.Status()
			if err != nil {
				log.Printf("%s - connect error: %v\n", server, err)
				return
			}
			if !utils.StringSliceContains(stat.Resources, cfg.Resource) {
				log.Printf("%s - '%s' not found\n", server, cfg.Resource)
				return
			}
			cs := zdapServerScore(stat)
			res, err := cli.GetResourceSnaps(cfg.Resource)
			if err != nil {
				log.Printf("%s - error getting snaps, error: %v\n", server, err)
				return
			}
			if cfg.ResourceFilter != "" {
				log.Printf("%s - filter %d snapshots using '%s'\n", server, len(res.Snaps), cfg.ResourceFilter)
				res.Snaps = slicez.Filter(res.Snaps, func(a zdap.PublicSnap) bool { return strings.Contains(a.Name, cfg.ResourceFilter) })
			}
			if len(res.Snaps) == 0 {
				log.Printf("%s - '%s' snapshot not found\n", server, cfg.Resource)
				return
			}
			log.Printf("%s - '%s' found with server score: %f (RAM: %d, Disk: %d, clones: %d, load: %f/%f/%f)\n", server, cfg.Resource, cs, stat.FreeMem, stat.FreeDisk, stat.Clones, stat.Load1, stat.Load5, stat.Load15)
			mu.Lock()
			for _, snap := range res.Snaps {
				availableSnaps = append(availableSnaps, availableSnap{
					PublicSnap: snap,
					cli:        cli,
					score:      cs - math.Log2(time.Since(snap.CreatedAt).Hours()/24), // Higher score for recent snapshots
				})
			}
			mu.Unlock()
		}(s)
	}
	wg.Wait()
	if len(availableSnaps) == 0 {
		log.Fatalf("ERROR: no zdap server with '%s' resource could be found!\n", cfg.Resource)
		return nil
	}
	availableSnaps = slicez.SortBy(availableSnaps, func(a, b availableSnap) bool {
		return a.score > b.score // Highest score first
	})
	for _, snap := range availableSnaps {
		log.Printf("%s - %s = %f\n", snap.cli.Server(), snap.Name, snap.score)
	}

	snap := availableSnaps[0]
	log.Printf("Creating a new clone on %s from %s\n", snap.cli.Server(), snap.Name)
	clone, err := snap.cli.CloneSnap(cfg.Resource, snap.CreatedAt, zdap.ClaimArgs{})
	if err != nil {
		log.Fatalf("ERROR: failed to clone '%s' resource, error %v\n", cfg.Resource, err)
	}

	return clone
}

func (p *k8sp) destroyClone(clone *zdap.PublicClone) {
	log.Printf("Destroying clone %s on %s (this should cause all open proxy connections to the server to be dropped)...\n", clone.Name, clone.Server)
	cfg := Config()
	cli := zdap.NewClient(http.DefaultClient, cfg.CloneOwnerName, fmt.Sprintf("%s:%d", clone.Server, cfg.APIPort))
	err := cli.DestroyClone(cfg.Resource, clone.CreatedAt)
	if err != nil {
		log.Printf("ERROR: failed to destroy clone %s on %s, error: %v\n", clone.Name, clone.Server, err)
		return
	}
	log.Printf("Clone %s on %s destroyed successfully\n", clone.Name, clone.Server)
}

func (p *k8sp) getExistingClone() *zdap.PublicClone {
	cfg := Config()
	var activeClones []zdap.PublicClone
	for _, s := range cfg.Servers {
		if !strings.Contains(s, ":") {
			s = fmt.Sprintf("%s:%d", s, cfg.APIPort)
		}

		c := zdap.NewClient(http.DefaultClient, cfg.CloneOwnerName, s)
		clones, err := c.GetClones(cfg.Resource)
		if err != nil {
			log.Printf("ERROR: failed to get '%s' resource clones from %s, error %v\n", cfg.Resource, s, err)
			continue
		}

		if len(clones) > 0 {
			activeClones = append(activeClones, clones...)
		}
	}

	if len(activeClones) == 0 {
		return nil
	}

	if len(activeClones) > 1 {
		// Sort available clones, so that we have the latest snap first
		activeClones = slicez.SortBy(activeClones, func(a, b zdap.PublicClone) bool {
			return a.SnappedAt.After(b.SnappedAt)
		})
	}

	return &activeClones[0]
}

func (p *k8sp) reset() {
	log.Printf("Trying to reset ZDAP resource %s...\n", Config().Resource)

	// Crate a new clone from the latest snapshot
	newClone := p.attachNewClone()
	if newClone == nil {
		return
	}

	// New snap attached as a new clone, update proxy to point to the new clone
	prevClone := p.clone
	p.clone = newClone
	p.proxy.TargetAddress = fmt.Sprintf("%s:%d", p.clone.Server, p.clone.Port)

	// Destroy the old clone, this will disconnect all open proxy connections against the old clone
	p.destroyClone(prevClone)
}

func (p *k8sp) setupResetTimer(ctx context.Context, atTimeStr string) {
	var atHH, atMM int
	if n, err := fmt.Sscanf(atTimeStr, "%2d%2d", &atHH, &atMM); n != 2 || err != nil {
		log.Printf("ERROR: failed to parse ZDAP_RESET_AT_HH_MM %s (n: %d, err: %v), reset timer disabled\n", atTimeStr, n, err)
		return
	}
	if atHH < 0 || atHH > 23 {
		log.Printf("ERROR: hour out of range (00..23): %d, reset timer disabled\n", atHH)
		return
	}
	if atMM < 0 || atMM > 59 {
		log.Printf("ERROR: minute out of range (00..59): %d, reset timer disabled\n", atHH)
		return
	}

	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h, m, _ := time.Now().Clock()
				if h == atHH && m == atMM {
					p.reset()
				}
			}
		}
	}()
}
