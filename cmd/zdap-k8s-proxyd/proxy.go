package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

type (
	TCPProxy struct {
		ListenPort    int
		TargetAddress string
		metric        *metric
	}
	metric struct {
		mu               sync.Mutex
		ActiveConnection int       `json:"active_connection"`
		TotalConnection  int       `json:"total_connection"`
		LastConnection   time.Time `json:"last_connection"`
		FirstConnection  time.Time `json:"first_connection"`
		CreatedAt        time.Time `json:"created_at"`
		Written          int64
		Read             int64
	}
)

func newProxy(listenPort int, serverAddress string, serverPort int) *TCPProxy {
	return &TCPProxy{
		ListenPort:    listenPort,
		TargetAddress: fmt.Sprintf("%s:%d", serverAddress, serverPort),
		metric: &metric{
			CreatedAt: time.Now(),
		},
	}
}

func (s TCPProxy) startMetricServer() {
	fmt.Printf("Starting metric server at udp://0.0.0.0:%d\n", s.ListenPort)

	conn, err := net.ListenUDP("udp", &net.UDPAddr{
		Port: s.ListenPort,
		IP:   net.ParseIP("0.0.0.0"),
	})
	if err != nil {
		log.Fatalf("ERROR: failed to start proxy metrics server, error: %v\n", err)
	}

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

		s.metric.mu.Lock()
		b, _ := json.Marshal(s.metric)
		s.metric.mu.Unlock()

		_, _ = conn.WriteToUDP(append(b, byte('\n')), addr)
	}

}

func (s TCPProxy) Run() {
	go s.startMetricServer()
	fmt.Printf("Starting zdap tcp proxy server at tcp://0.0.0.0:%d, targeting tcp://%s\n", s.ListenPort, s.TargetAddress)
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", s.ListenPort))
	if err != nil {
		log.Fatalf("ERROR: failed to open proxy listening TCP port: %d, error: %v\n", s.ListenPort, err)
	}

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
	s.metric.mu.Lock()
	s.metric.ActiveConnection += 1
	s.metric.TotalConnection += 1
	s.metric.LastConnection = time.Now()
	if s.metric.FirstConnection.IsZero() {
		s.metric.FirstConnection = time.Now()
	}
	s.metric.mu.Unlock()

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
		s.metric.mu.Lock()
		s.metric.Written += w
		s.metric.mu.Unlock()
	}()
	go func() {
		r, _ := io.Copy(in, out)
		fmt.Println("closing in", in.Close())
		s.metric.mu.Lock()
		s.metric.ActiveConnection -= 1
		s.metric.Read += r
		s.metric.mu.Unlock()
	}()
}
