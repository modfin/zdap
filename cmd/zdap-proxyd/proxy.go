package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

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
	ListenPort      int
	TargetAddress   string
	Metric          *Metric
	metricConn      *net.UDPConn
	proxyConn       *net.Listener
	useMetricServer bool
}

func (s *TCPProxy) startMetricServer() {
	log.Printf("Starting metric server at udp://0.0.0.0:%d\n", s.ListenPort)

	conn, err := net.ListenUDP("udp", &net.UDPAddr{
		Port: s.ListenPort,
		IP:   net.ParseIP("0.0.0.0"),
	})

	check(err)

	s.metricConn = conn
	for {
		var buf [2048]byte
		n, addr, err := conn.ReadFromUDP(buf[0:])
		if err != nil {
			log.Println("metric udp read error:", err)
		}

		payload := strings.ToLower(strings.TrimSpace(string(buf[:n])))

		if payload != "metrics" {
			log.Println("got wrong udp metric command", payload)
			_, err = conn.WriteToUDP([]byte("{\"error\": \"only the word 'metrics' is to be send in order to get a response\"}\n"), addr)
			if err != nil {
				log.Println("could not respond do udp")
			}
			continue
		}

		s.Metric.mu.Lock()
		b, _ := json.Marshal(s.Metric)
		s.Metric.mu.Unlock()

		_, _ = conn.WriteToUDP(append(b, byte('\n')), addr)
	}

}

func (s *TCPProxy) Start(_ context.Context) {
	if s.useMetricServer {
		go s.startMetricServer()
	}

	log.Printf("Starting zdap tcp proxy server at tcp://0.0.0.0:%d, targeting tcp://%s\n", s.ListenPort, s.TargetAddress)
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", s.ListenPort))
	check(err)
	s.proxyConn = &listener
	go func() {
		defer listener.Close()

		for {
			in, err := listener.Accept()
			if err != nil {
				log.Println("could not accept connection,", err)
				continue
			}
			go s.proxy(in)
		}
	}()
}

func (s TCPProxy) Stop() {
	if s.proxyConn != nil {
		(*s.proxyConn).Close()
	}
	if s.metricConn != nil {
		s.metricConn.Close()
	}
}

func (s TCPProxy) proxy(in net.Conn) {
	if s.useMetricServer {
		s.Metric.mu.Lock()
		s.Metric.ActiveConnection += 1
		s.Metric.TotalConnection += 1
		s.Metric.LastConnection = time.Now()
		if s.Metric.FirstConnection.IsZero() {
			s.Metric.FirstConnection = time.Now()
		}
		s.Metric.mu.Unlock()
	}
	log.Println("Accepted connection - Dialing recipient")
	out, err := net.Dial("tcp", s.TargetAddress)
	if err != nil {
		log.Println("could not dial target", s.TargetAddress)
		_ = in.Close()
		return
	}

	go func() {
		w, _ := io.Copy(out, in)
		log.Println("closing out", out.Close())
		if s.useMetricServer {
			s.Metric.mu.Lock()
			s.Metric.Written += w
			s.Metric.mu.Unlock()
		}
	}()
	go func() {
		r, _ := io.Copy(in, out)
		log.Println("closing in", in.Close())
		if s.useMetricServer {
			s.Metric.mu.Lock()
			s.Metric.ActiveConnection -= 1
			s.Metric.Read += r
			s.Metric.mu.Unlock()
		}
	}()
}
