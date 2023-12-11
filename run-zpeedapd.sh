#!/bin/bash

export NETWORK_ADDRESS=10.200.1.212
export ZPOOL=zdap-pool/databases
export API_PORT=43210
export CONFIG_DIR=/zdap/config

go run cmd/zdapd/zdapd.go $@
