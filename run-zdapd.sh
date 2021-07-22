#!/bin/bash
sudo env $(cat .env-zdap | xargs) go run cmd/zdapd/zdapd.go $@