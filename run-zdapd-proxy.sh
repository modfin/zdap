#!/bin/bash
env $(cat .env-zdap-proxy | xargs) go run cmd/zdap-proxyd/zdap-proxyd.go