
`zdap`, `zfs-database-access-proxy`, is a commandline tool and server ware to offload large databases in a docker compose dev environment to other machines. 

# Background
With a sample size of 1, our experience at Modular Finance, is as follows
 
We often use, censored, production data in our development environment and access to the full production databases is often required for efficient development. Starting out, we had databases which were around 5-10gb, just cloning them every once in a while to your local environment add adding restoring them to a postgres docker image was little trouble. However, time goes on and now we have multiple databases running at more than 200gb which makes cloning a much more tidies process which can take multiple hours.

## Environment 
Historically most have of us has used stationary computers in order to handle the load of these large datasets needed for much of the daily work. Covid and the general growth of the company has made flexibility and laptops much more of a requirement. 

We have for a very long time been completely dockerized, which to us means, that we run our development environment insider docker containers using docker-compose, a long with k8s for production. Our databases for our development environment is simply postgres images running through docker-compose with a mounted volume.     

## Concept
The basic concept for `zdap` is to override a docker-compose file with a proxy, that offloads the database, to a server.

Everyone want there own databses, so the basic idea is for our server to clone the database into a zfs volume. Since zfs has copy-on-write support, we can utilize snapshots and zfs-clones to provide everyone with their own database instance with zero overhead. 


# Components
There are three components to `zdap`
* `zdapd`       - server 
* `zdap`        - client commandline tool
* `zdap-proxyd` - internal proxy

 
# zdapd

`zdapd` is the daemon running on the database server that exposes a http api for management.

## Dependency
* `zfs`
* `zfs dev`
* `docker`

## zfs dependency  
Install zfs
```bash 
apt-get install zfsutils-linux libzfslinux-dev
```

Setting up a pool for zdap to use for storing the databases
```bash 
zpool create zdap-pool /dev/sdx1 /dev/sdx2 ...
zpool list 
#NAME        SIZE   ALLOC   FREE  CKPOINT  EXPANDSZ   FRAG    CAP  DEDUP    HEALTH  ALTROOT
#zdap-pool  4,56T  94,5K   4,56T        -         -     0%     0%  1.00x    ONLINE  -

zfs list
#NAME        USED  AVAIL     REFER  MOUNTPOINT
#zdap-pool    87K  4,56T       24K  /zdap-pool

```


## Installing


```bash
## Installing
go install github.com/modfin/zdap/cmd/zdapd@latest

## Running
zdapd --zpool=zdap-pool \
      --config-dir=/path/to/config/dir \
      --network-address=<ip address of the machine> \
      --api-port=43210 \
      serv
```


# zdap

`zdap` is the cli tool that is used by a user in order to create instances of database and attach them to the docker-compose environment    

## Installing & Running

```bash 
## Installing
go install github.com/modfin/zdap/cmd/zdap@latest

zdap auto-complete [bash|zsh|fish] # prints auto-compleat installation instructions

zdap set user <name@host>
zdap add origin <ip>:<port> # you can add mutiple origins and this way zdap balences
                            # resource creation over all servers running zdapd.

## in you project where a docker-compose.yml file is located
zdap init               # initilizes zdap for the docker-compose context
zdap list resources     # list the databases that can be mounted
zdap attach <resource>  # creates a clone of the resource in the at the zdapd 
                        # server and attaches it to docker-compose.override file.
                        # docker-compose up <resource> will now use the zdapd server
                        # instance of the database
zdap detach <resource>  # detaches the resource from the docker-compose.override 
                        # and destroys the resource-clone on the zdap server
```

# kubernetes
To access a zdap database from within a kubernetes cluster, just deploy a zdap-proxy image where the
`ZDAP_CLONE_OWNER_NAME`, `ZDAP_RESOURCE` and `ZDAP_SERVERS` environment variable are set. `ZDAP_CLONE_OWNER_NAME` must 
a be unique identifier for the cluster.

There is also another optional
parameter (`ZDAP_RESET_AT_HH_MM`) that can be set to automatically take a new snapshot each day.

Deployment example:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres-x
spec:
  replicas: 1
  selector:
    matchLabels:
      app: postgres-x
  strategy:
    type: Recreate
  template:
    metadata:
      labels:
        app: postgres-x
    spec:
      containers:
        - name: postgres-x
          image: modfin/zdap-proxy:latest
          env:
            - name: ZDAP_CLONE_OWNER_NAME
              value: "staging@example.com"
            - name: ZDAP_RESOURCE
              value: "postgres-x"
            - name: ZDAP_SERVERS
              value: "192.168.0.x,192.168.0.y"
            - name: ZDAP_RESET_AT_HH_MM
              value: "0130"
```

