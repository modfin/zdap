package main

import "time"

func dockerProxy() *TCPProxy {
	return &TCPProxy{
		ListenPort:    Config().ListenPort,
		TargetAddress: Config().TargetAddress,
		Metric: &Metric{
			CreatedAt: time.Now(),
		},
		useMetricServer: true,
	}
}
