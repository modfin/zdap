package main

import (
	"encoding/json"
	"fmt"
	"github.com/caarlos0/env"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

func main() {

	p := TCPProxy{
		ListenPort:    Config().ListenPort,
		TargetAddress: Config().TargetAddress,
		Metric: &Metric{
			CreatedAt: time.Now(),
		},
	}
	p.Start()

}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

type conf struct {
	ListenPort    int    `env:"LISTEN_PORT"`
	TargetAddress string `env:"TARGET_ADDRESS"`
}

var (
	once sync.Once
	cfg  conf
)

func Config() *conf {
	once.Do(func() {
		cfg = conf{}
		if err := env.Parse(&cfg); err != nil {
			log.Panic("Couldn't parse Config from env: ", err)
		}
	})
	return &cfg
}

type Metric struct {
	mu               sync.Mutex
	ActiveConnection int       `json:"active_connection"`
	TotalConnection  int       `json:"total_connection"`
	LastConnection   time.Time `json:"last_connection"`
	FirstConnection  time.Time `json:"first_connection"`
	CreatedAt        time.Time `json:"created_at"`
	Written          int64
	Read             int64
}

type TCPProxy struct {
	ListenPort    int
	TargetAddress string
	Metric        *Metric
}

func (s TCPProxy) startMetricServer() {
	fmt.Printf("Starting metric server at udp://0.0.0.0:%d\n", s.ListenPort)

	conn, err := net.ListenUDP("udp", &net.UDPAddr{
		Port: s.ListenPort,
		IP:   net.ParseIP("0.0.0.0"),
	})

	check(err)
	for {
		var buf [2048]byte
		n, addr, err := conn.ReadFromUDP(buf[0:])
		if err != nil {
			fmt.Println("metric udp read error:", err)
		}

		payload := strings.ToLower(strings.TrimSpace(string(buf[:n])))

		if payload != "metrics" {
			fmt.Println("got wrong udp metric command", payload)
			_, err = conn.WriteToUDP([]byte("{\"error\": \"only the word 'metrics' is to be send in order to get a response\"}\n"), addr)
			if err != nil {
				fmt.Println("could not respond do udp")
			}
			continue
		}

		s.Metric.mu.Lock()
		b, _ := json.Marshal(s.Metric)
		s.Metric.mu.Unlock()

		_, _ = conn.WriteToUDP(append(b, byte('\n')), addr)
	}

}

func (s TCPProxy) Start() {

	go s.startMetricServer()
	fmt.Printf("Starting zdap tcp proxy server at tcp://0.0.0.0:%d, targeting tcp://%s\n", s.ListenPort, s.TargetAddress)
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", s.ListenPort))
	check(err)
	defer listener.Close()

	for {
		in, err := listener.Accept()
		if err != nil {
			fmt.Println("could not accept connection,", err)
			continue
		}
		fmt.Println("Accepted connection")
		go s.proxy(in)
	}
}

func (s TCPProxy) proxy(in net.Conn) {
	s.Metric.mu.Lock()
	s.Metric.ActiveConnection += 1
	s.Metric.TotalConnection += 1
	s.Metric.LastConnection = time.Now()
	if s.Metric.FirstConnection.IsZero() {
		s.Metric.FirstConnection = time.Now()
	}
	s.Metric.mu.Unlock()

	fmt.Println(" Dialing recipient")
	out, err := net.Dial("tcp", s.TargetAddress)
	if err != nil {
		fmt.Println("could not dial target", s.TargetAddress)
		_ = in.Close()
		return
	}

	go func() {
		w, _ := io.Copy(out, in)
		fmt.Println("closing out", out.Close())
		s.Metric.mu.Lock()
		s.Metric.Written += w
		s.Metric.mu.Unlock()
	}()
	go func() {
		r, _ := io.Copy(in, out)
		fmt.Println("closing in", in.Close())
		s.Metric.mu.Lock()
		s.Metric.ActiveConnection -= 1
		s.Metric.Read += r
		s.Metric.mu.Unlock()
	}()
}
