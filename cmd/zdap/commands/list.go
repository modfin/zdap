package commands

import (
	"fmt"
	"github.com/c2h5oh/datasize"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal/utils"
	"github.com/urfave/cli/v2"
	"net/http"
	"strings"
	"time"
)

func filerNonLocalResource(resource []zdap.PublicResource) ([]zdap.PublicResource, error) {
	local, err := GetLocalResources()
	if err != nil {
		return nil, err
	}
	var localResource []zdap.PublicResource
	for _, r := range resource {
		if utils.StringSliceContains(local, r.Name) {
			localResource = append(localResource, r)
		}
	}
	return localResource, err
}

func ListResourceData(all bool, verbose bool) ([]zdap.PublicResource, error) {
	cfg, err := getConfig()
	if err != nil {
		return nil, err
	}

	var allResources []zdap.PublicResource
	for _, s := range cfg.Servers {
		client := zdap.NewClient(http.DefaultClient, cfg.User, s)
		resources, err := client.GetResources()
		if err != nil {
			if verbose {
				fmt.Printf("@%s [COULD NOT CONNECT, %v]\n", s, err)
			}
			continue
		}

		if !all {
			resources, err = filerNonLocalResource(resources)
			if err != nil {
				return nil, err
			}
		}
		allResources = append(allResources, resources...)
	}

	return allResources, nil
}

func ListResources(c *cli.Context) error {
	cfg, err := getConfig()
	if err != nil {
		return err
	}

	for _, s := range cfg.Servers {
		client := zdap.NewClient(http.DefaultClient, cfg.User, s)
		resources, err := client.GetResources()
		if err != nil {
			fmt.Printf("@%s [COULD NOT CONNECT, %v]\n", s, err)
			continue
		}

		if !c.Bool("all") {
			resources, err = filerNonLocalResource(resources)
			if err != nil {
				return err
			}
		}

		r1 := "├"
		fmt.Printf("@%s\n", s)
		for i, resource := range resources {
			if i == len(resources)-1 {
				r1 = "└"
			}
			fmt.Printf("%s %s", r1, resource.Name)
			if resource.ClonePool.MinClones > 0 {
				fmt.Printf(
					" (pool: min_clones=%d, max_clones=%d, max_lease_time=%ds, default_lease_time=%ds)",
					resource.ClonePool.MinClones,
					resource.ClonePool.MaxClones,
					resource.ClonePool.ClaimMaxTimeoutSeconds,
					resource.ClonePool.DefaultTimeoutSeconds,
				)
			}
			fmt.Printf("\n")
		}
	}
	return nil
}
func ResourceListCompletion(c *cli.Context) {
	cfg, err := getConfig()
	if err != nil {
		return
	}

	m := map[string]struct{}{}
	for _, s := range cfg.Servers {
		client := zdap.NewClient(http.DefaultClient, cfg.User, s)
		resources, err := client.GetResources()
		if err != nil {
			continue
		}
		if !c.Bool("all") {
			resources, err = filerNonLocalResource(resources)
			if err != nil {
				continue
			}
		}
		for _, r := range resources {
			m[r.Name] = struct{}{}
		}
	}
	for name, _ := range m {
		if utils.StringSliceContains(c.Args().Slice(), name) {
			continue
		}
		fmt.Println(name)
	}
}
func ListSnaps(c *cli.Context) error {
	cfg, err := getConfig()
	if err != nil {
		return err
	}

	var name string
	if c.Args().Len() > 0 {
		name = c.Args().First()
	}

	for _, s := range cfg.Servers {
		client := zdap.NewClient(http.DefaultClient, cfg.User, s)
		resources, err := client.GetResources()
		if err != nil {
			fmt.Printf("@%s [COULD NOT CONNECT, %v]\n", s, err)
			continue
		}

		if !c.Bool("all") {
			resources, err = filerNonLocalResource(resources)
			if err != nil {
				return err
			}
		}

		fmt.Printf("@%s\n", s)
		r1 := "├"
		rPipe := "│"
		for i, resource := range resources {
			if name != "" && name != resource.Name {
				continue
			}
			if i == len(resources)-1 {
				r1 = "└"
				rPipe = " "
			}
			s1 := "├"
			fmt.Printf("%s %s\n", r1, resource.Name)
			for j, snap := range resource.Snaps {
				if j == len(resource.Snaps)-1 {
					s1 = "└"
				}
				fmt.Printf("%s %s %s\n", rPipe, s1, snap.CreatedAt.In(time.UTC).Format(utils.TimestampFormat))
			}
		}
	}
	return nil
}
func ListClones(c *cli.Context) error {
	cfg, err := getConfig()
	if err != nil {
		return err
	}

	//var servers []string
	var lookingForResources []string
	//var cloneName time.Time

	for _, arg := range c.Args().Slice() {
		//if strings.HasPrefix(arg, "@") {
		//	servers = append(servers, arg[1:])
		//	continue
		//}
		//if utils.TimestampFormatRegexp.MatchString(arg) {
		//	cloneName, err = time.Parse(utils.TimestampFormat, arg)
		//	if err != nil {
		//		return err
		//	}
		//	continue
		//}
		lookingForResources = append(lookingForResources, arg)
	}
	//if len(servers) == 0 {
	//	servers = cfg.Servers
	//}

	for _, s := range cfg.Servers {
		client := zdap.NewClient(http.DefaultClient, cfg.User, s)
		resources, err := client.GetResources()
		if err != nil {
			fmt.Printf("@%s [COULD NOT CONNECT, %v]\n", s, err)
			continue
		}

		if !c.Bool("all") {
			resources, err = filerNonLocalResource(resources)
			if err != nil {
				return err
			}
		}

		var res []zdap.PublicResource
		for _, resource := range resources {
			if len(lookingForResources) > 0 && !utils.StringSliceContains(lookingForResources, resource.Name) {
				continue
			}

			var snaps []zdap.PublicSnap
			for _, snap := range resource.Snaps {
				var clones []zdap.PublicClone
				for _, clone := range snap.Clones {
					//if !cloneName.IsZero() && !cloneName.Equal(clone.CreatedAt) {
					//	continue
					//}
					clones = append(clones, clone)
				}
				snap.Clones = clones

				if len(snap.Clones) > 0 {
					snaps = append(snaps, snap)
				}
			}
			resource.Snaps = snaps

			res = append(res, resource)
		}

		switch c.String("format") {
		case "yaml":
			for _, resource := range res {
				for _, snaps := range resource.Snaps {
					for _, clone := range snaps.Clones {
						fmt.Println(clone.YAML(5432))
						fmt.Println()
					}
				}
			}

		default:
			fmt.Printf("@%s\n", s)

			r1 := "├"
			rPipe := "│"
			sPipe := "│"

			for r, resource := range res {
				lastR := r == len(res)-1
				if lastR {
					r1 = "└"
					rPipe = " "
				}
				fmt.Printf("%s %s\n", r1, resource.Name)
				s1 := "├"
				for s, snaps := range resource.Snaps {
					lastS := s == len(resource.Snaps)-1
					if lastS {
						s1 = "└"
						sPipe = rPipe
					}
					fmt.Printf("%s %s %s\n", rPipe, s1, snaps.CreatedAt.In(time.UTC).Format(utils.TimestampFormat))
					c1 := "├"
					for c, clone := range snaps.Clones {
						if c == len(snaps.Clones)-1 {
							c1 = "└"
						}
						fmt.Printf("%s %s %s %s\n", rPipe, sPipe, c1, clone.CreatedAt.In(time.UTC).Format(utils.TimestampFormat))
					}
				}
			}
		}

	}
	return nil
}

func ListOrigins(c *cli.Context) error {
	cfg, err := getConfig()
	if err != nil {
		return err
	}

	verbose := c.Bool("verbose")

	for _, s := range cfg.Servers {
		c := zdap.NewClient(http.DefaultClient, cfg.User, s)
		stat, err := c.Status()
		if err != nil {
			fmt.Printf("@%s [COULD NOT CONNECT]\n", s)
			continue
		}
		fmt.Printf("@%s [CONNECTED]\n", s)
		if verbose {
			fmt.Printf("├ Resources: %s \n", strings.Join(stat.Resources, ", "))
			fmt.Printf("├ Snaps: %d \n", stat.Snaps)
			fmt.Printf("├ Clones: %d \n", stat.Clones)
			fmt.Printf("├ Disk Used: %s \n", (datasize.ByteSize(stat.UsedDisk) * datasize.B).HumanReadable())
			fmt.Printf("├ Disk Free: %s \n", (datasize.ByteSize(stat.FreeDisk) * datasize.B).HumanReadable())
			fmt.Printf("├ Disk Total: %s \n", (datasize.ByteSize(stat.TotalDisk) * datasize.B).HumanReadable())
			fmt.Printf("├ Mem Used: %s \n", (datasize.ByteSize(stat.UsedMem) * datasize.B).HumanReadable())
			fmt.Printf("├ Mem Free: %s \n", (datasize.ByteSize(stat.FreeMem) * datasize.B).HumanReadable())
			fmt.Printf("├ Mem Cached: %s \n", (datasize.ByteSize(stat.CachedMem) * datasize.B).HumanReadable())
			fmt.Printf("├ Mem Total: %s \n", (datasize.ByteSize(stat.TotalMem) * datasize.B).HumanReadable())
			fmt.Printf("├ Load 1: %.2f \n", stat.Load1)
			fmt.Printf("├ Load 5: %.2f \n", stat.Load5)
			fmt.Printf("└ Load 15: %.2f \n", stat.Load15)

		}
	}

	return nil
}
