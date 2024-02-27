package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/c2h5oh/datasize"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal/compose"
	"github.com/modfin/zdap/internal/utils"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
	"io/ioutil"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

func parsArgs(args []string) (servers []string, resource string, snap time.Time, err error) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "@") {
			servers = append(servers, arg[1:])
			continue
		}
		if utils.TimestampFormatRegexp.MatchString(arg) {
			snap, err = time.Parse(utils.TimestampFormat, arg)
			if err != nil {
				return
			}
			continue
		}
		resource = arg
	}
	return
}

func findServerCandidate(resource string, user string, servers []string, favorPooled bool) (string, error) {

	var candidates []zdap.ServerStatus
	for _, s := range servers {
		stat, err := zdap.NewClient(user, s).Status()
		if err != nil {
			fmt.Println("[Error connecting to server]", err)
			continue
		}
		if utils.StringSliceContains(stat.Resources, resource) {
			stat.Address = s
			candidates = append(candidates, *stat)
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("could not find candidate origin for %s resource", resource)
	}

	score := func(stat zdap.ServerStatus) float64 { // higher the better
		disk := stat.FreeDisk
		clones := stat.Clones
		mem := stat.FreeMem
		load := stat.Load15

		sum := math.Log2(float64(disk) / float64(datasize.GB) / 100.0) // more disk is good
		sum += math.Log2(float64(mem) / float64(datasize.GB))          // mode ram is good
		if clones > 0 {
			sum -= math.Log2(float64(clones)) // less clones is good
		}
		if load > 0 {
			sum -= math.Log2(load) // load less than 1 is good
		}
		return sum
	}

	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]

		if favorPooled {
			return a.ResourceDetails[resource].PooledClonesAvailable > b.ResourceDetails[resource].PooledClonesAvailable
		}
		return score(a) > score(b)
	})

	return candidates[0].Address, nil
}
func DestroyCloneCompletion(c *cli.Context) {
	AttachCloneCompletion(c)
}

func DestroyClone(c *cli.Context) error {
	return destroyClone(c.Args().Slice())
}
func destroyClone(args []string) error {
	var err error

	cfg, err := getConfig()
	if err != nil {
		return err
	}

	servers, resource, clone, err := parsArgs(args)
	if err != nil {
		return err
	}

	if len(servers) == 0 {
		servers = cfg.Servers
	}

	if len(resource) == 0 {
		return errors.New("clone resource must be provided")
	}

	plural := "clones"
	if !clone.IsZero() {
		plural = "clone " + clone.Format(utils.TimestampFormat)
	}

	for _, s := range servers {
		fmt.Printf("Destroying %s of %s @%s \n", plural, resource, s)
		client := zdap.NewClient(cfg.User, s)
		err = client.DestroyClone(resource, clone)
		if err != nil {
			fmt.Println("[Err]", err)
			err = nil
			continue
		}
	}

	return nil
}

func CloneResourceCompletion(c *cli.Context) {
	AttachCloneCompletion(c)
}

func CloneResource(c *cli.Context) error {
	clone, err := cloneResource(c.Args().Slice(), zdap.ClaimArgs{})
	if err != nil {
		return err
	}
	fmt.Println("Attach to project by running, run:")
	fmt.Printf("zdap attach --new=false @%s:%d %s %s\n", clone.Server, clone.Port, clone.Resource, clone.CreatedAt.Format(utils.TimestampFormat))
	return nil
}

type ClaimResult struct {
	Server  string `json:"server"`
	Port    int    `json:"port"`
	CloneId string `json:"clone_id"`
}

func ExpireClaimedResource(c *cli.Context) error {
	args := c.Args().Slice()
	if len(args) < 2 {
		return errors.New("no claim specified")
	}

	resource := args[0]
	claimId := args[1]

	var err error

	cfg, err := getConfig()
	if err != nil {
		return err
	}

	servers := cfg.Servers

	for _, s := range servers {
		client := zdap.NewClient(cfg.User, s)
		err = client.ExpireClaim(resource, claimId)
		if err != nil {
			fmt.Println("[Err]", err)
			err = nil
			continue
		}
	}

	return nil

}

func ClaimResource(c *cli.Context) error {
	ttl := c.Int64("ttl")
	clone, err := cloneResource(c.Args().Slice(), zdap.ClaimArgs{
		ClaimPooled: true,
		TtlSeconds:  ttl,
	})
	if err != nil {
		return err
	}
	b, err := json.Marshal(ClaimResult{
		Server:  clone.Server,
		Port:    clone.Port,
		CloneId: clone.Name,
	})

	fmt.Println(string(b))
	return nil
}

func cloneResource(args []string, claimArgs zdap.ClaimArgs) (*zdap.PublicClone, error) {
	var err error

	cfg, err := getConfig()
	if err != nil {
		return nil, err
	}

	servers, resource, snap, err := parsArgs(args)
	if err != nil {
		return nil, err
	}

	if resource == "" {
		return nil, errors.New("a resource must be provided as an argument")
	}

	var server string
	if len(servers) > 0 {
		server = servers[0]
	}
	if len(servers) == 0 {
		server, err = findServerCandidate(resource, cfg.User, cfg.Servers, claimArgs.ClaimPooled)
		if err != nil {
			return nil, fmt.Errorf("could not find a suitable server, %w", err)
		}
	}
	client := zdap.NewClient(cfg.User, server)
	clone, err := client.CloneSnap(resource, snap, claimArgs)
	if err != nil {
		return nil, err
	}

	return clone, nil
}

func findClone(servers []string, resource string, cloneName time.Time) (clone *zdap.PublicClone, err error) {
	cfg, err := getConfig()
	if err != nil {
		return nil, err
	}
	clone = &zdap.PublicClone{}
	for _, server := range servers {
		var resources []zdap.PublicResource
		resources, err = zdap.NewClient(cfg.User, server).GetResources()
		if err != nil {
			fmt.Printf("[Error connecting to %s] %v", server, err)
		}

		for _, res := range resources {
			if res.Name == resource {
				for _, snap := range res.Snaps {
					for _, c := range snap.Clones {
						// Take the latest clone if a specific clone is not given
						if cloneName.IsZero() {
							if c.CreatedAt.After(clone.CreatedAt) {
								clone = &c
							}
							continue
						}
						if c.CreatedAt.Equal(cloneName) {
							clone = &c
							return
						}
					}
				}
			}
		}

	}
	return
}

func AttachCloneCompletion(c *cli.Context) {
	servers, resource, clone, err := parsArgs(c.Args().Slice())
	if err != nil {
		return
	}

	resources, err := ListResourceData(false, false)
	if err != nil {
		return
	}
	var complets []string

	if resource == "" {
		for _, res := range resources {
			complets = append(complets, res.Name)
		}
	}

	var server string
	if len(servers) > 0 {
		server = servers[0]
	}

	if clone.IsZero() && len(resource) != 0 {
		for _, res := range resources {
			if res.Name != resource {
				continue
			}
			for _, s := range res.Snaps {
				for _, c := range s.Clones {
					complets = append(complets, c.CreatedAt.Format(utils.TimestampFormat))
				}
			}
		}
	}

	if server == "" && !clone.IsZero() && len(resource) != 0 {
		for _, res := range resources {
			if res.Name != resource {
				continue
			}
			for _, s := range res.Snaps {
				for _, c := range s.Clones {
					if !clone.Equal(c.CreatedAt) {
						continue
					}
					complets = append(complets, fmt.Sprintf("%s:%d", c.Server, c.APIPort))
				}
			}
		}
	}
	fmt.Println(strings.Join(complets, "\n"))
}
func AttachClone(c *cli.Context) error {

	var err error
	cfg, err := getConfig()
	if err != nil {
		return err
	}

	servers, resource, cloneName, err := parsArgs(c.Args().Slice())
	if err != nil {
		return err
	}

	settings, err := LoadSettings()
	if err != nil {
		return err
	}
	composeData, err := ioutil.ReadFile(settings.Compose)
	if err != nil {
		return err
	}
	var docker compose.DockerCompose
	err = yaml.Unmarshal(composeData, &docker)
	if err != nil {
		return err
	}

	overrideData, err := ioutil.ReadFile(settings.Override)
	if err != nil {
		return err
	}
	var override compose.DockerCompose
	err = yaml.Unmarshal(overrideData, &override)
	if err != nil {
		return err
	}

	if override.Services == nil {
		override.Services = map[string]*compose.Container{}
	}

	original := docker.Services[resource]
	if original == nil && !c.Bool("force") {
		return fmt.Errorf("the resource, %s is not present in original docker compose file, %s", resource, settings.Compose)
	}

	current := override.Services[resource]
	if current != nil {
		return fmt.Errorf("the resource is alread attached, use zdap detach to remove it first")
	}

	if len(servers) == 0 {
		servers = cfg.Servers
	}

	var clone *zdap.PublicClone

	if c.Bool("new") {
		fmt.Print("Cloning ", resource, "...")
		clone, err = cloneResource(c.Args().Slice(), zdap.ClaimArgs{
			ClaimPooled: c.Bool("claim"),
			TtlSeconds:  c.Int64("ttl"),
		})
		if err != nil {
			return err
		}
		fmt.Println("done")
	}

	if clone == nil {
		fmt.Print("Finding clone for ", resource, "...")
		clone, err = findClone(servers, resource, cloneName)
		if err != nil {
			return err
		}
		fmt.Println("done", clone.Name, clone.SnappedAt)
	}

	if clone.CreatedAt.IsZero() {
		return fmt.Errorf("could not find any clone to attach")
	}

	name := clone.Resource

	var ports []string
	if original != nil {
		ports = original.Ports
	}

	port := c.Int("port")
	if port == 0 && len(ports) > 0 {
		p, err := strconv.ParseInt(strings.Split(ports[0], ":")[1], 10, 32)
		if err != nil {
			return err
		}
		port = int(p)
	}
	if port == 0 {
		port = 5432
	}

	container := compose.Container{}
	container.Image = "crholm/zdap-proxy:latest"
	container.Ports = ports
	container.Environment = []string{
		fmt.Sprintf("LISTEN_PORT=%d", port),
		fmt.Sprintf("TARGET_ADDRESS=%s:%d", clone.Server, clone.Port),
	}

	labels := []string{
		fmt.Sprintf("zdap.resource=%s", clone.Resource),
		fmt.Sprintf("zdap.clone=%s", clone.CreatedAt.Format(utils.TimestampFormat)),
		fmt.Sprintf("zdap.origin=%s", clone.Server),
		fmt.Sprintf("zdap.api_port=%d", clone.APIPort),
		fmt.Sprintf("zdap.port=%d", clone.Port),
	}
	container.Labels = labels

	override.Services[name] = &container
	if override.Version == "" {
		override.Version = docker.Version
	}

	overrideData, err = yaml.Marshal(override)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(settings.Override, overrideData, 0644)
}

func DetachCloneCompletion(c *cli.Context) {
	settings, err := LoadSettings()
	if err != nil {
		return
	}
	overrideData, err := ioutil.ReadFile(settings.Override)
	if err != nil {
		return
	}
	var override compose.DockerCompose
	err = yaml.Unmarshal(overrideData, &override)
	if err != nil {
		return
	}
	args := c.Args().Slice()
	for k, _ := range override.Services {
		if utils.StringSliceContains(args, k) {
			continue
		}
		fmt.Println(k)
	}
}

func DetachClone(c *cli.Context) error {

	var err error

	resources := c.Args().Slice()

	settings, err := LoadSettings()
	if err != nil {
		return err
	}
	composeData, err := ioutil.ReadFile(settings.Compose)
	if err != nil {
		return err
	}
	var docker compose.DockerCompose
	err = yaml.Unmarshal(composeData, &docker)
	if err != nil {
		return err
	}

	overrideData, err := ioutil.ReadFile(settings.Override)
	if err != nil {
		return err
	}
	var override compose.DockerCompose
	err = yaml.Unmarshal(overrideData, &override)
	if err != nil {
		return err
	}

	if override.Services == nil {
		override.Services = map[string]*compose.Container{}
	}

	for _, resource := range resources {

		original := docker.Services[resource]
		if original == nil && !c.Bool("force") {
			return fmt.Errorf("the resource, %s is not present in original docker compose file, %s", resource, settings.Compose)
		}

		current := override.Services[resource]
		if current == nil {
			return fmt.Errorf("the resource, %s, you are trying to detach does not exist in overrides", resource)
		}

		labels := map[string]string{}

		rawLabels, ok := current.Labels.([]interface{})
		if !ok {
			fmt.Println(reflect.TypeOf(current.Labels))
			return fmt.Errorf("labels are missing for override to get the compleat context")
		}
		for _, l := range rawLabels {
			label, ok := l.(string)
			if !ok {
				continue
			}
			parts := strings.Split(label, "=")
			if len(parts) != 2 {
				continue
			}
			labels[parts[0]] = parts[1]
		}

		if c.Bool("destroy") {
			err := destroyClone([]string{
				labels["zdap.resource"],
				labels["zdap.clone"],
				fmt.Sprintf("@%s:%s", labels["zdap.origin"], labels["zdap.api_port"]),
			})
			if err != nil {
				fmt.Printf("Error destoying clone, %v\n", err)
				err = nil
			}
		}

		delete(override.Services, resource)
	}
	overrideData, err = yaml.Marshal(override)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(settings.Override, overrideData, 0644)
}
