package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	log.Println("Initializing proxy...")
	appCtx := context.Background()

	var proxy interface {
		Start(context.Context)
		Stop()
	}

	if useK8sProxy() {
		proxy = k8sProxy()
	} else {
		proxy = dockerProxy()
	}

	proxy.Start(appCtx)

	log.Println("Proxy started")

	ctx, cancel := signal.NotifyContext(appCtx, syscall.SIGINT, syscall.SIGTERM)
	<-ctx.Done()
	cancel()

	log.Println("Shutting down proxy")

	proxy.Stop()

	log.Println("Proxy stopped!")
}
