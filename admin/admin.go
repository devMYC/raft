package main

import (
	"log"
	"net/http"
	"os/exec"
)

const (
    port = ":9090"
    sh = "/bin/sh"
)

func blockTraffic(w http.ResponseWriter, req *http.Request) {
	in := "iptables -A INPUT -s 172.18.0.0/16 -j DROP"
	out := "iptables -A OUTPUT -d 172.18.0.0/16 -j DROP"
	block := in + "; " + out
	cmd := exec.Command(sh, "-c", block)
	if _, err := cmd.Output(); err != nil {
		log.Printf("[ERROR] Failed to execute: %s\n", block)
		w.WriteHeader(500)
	} else {
		log.Printf("[OK] %s\n", block)
		w.WriteHeader(204)
	}
}

func unblockTraffic(w http.ResponseWriter, req *http.Request) {
	in := "iptables -D INPUT -s 172.18.0.0/16 -j DROP"
	out := "iptables -D OUTPUT -d 172.18.0.0/16 -j DROP"
	unblock := out + "; " + in
	cmd := exec.Command(sh, "-c", unblock)
	if _, err := cmd.Output(); err != nil {
		log.Printf("[ERROR] Failed to execute: %s\n", unblock)
		w.WriteHeader(500)
	} else {
		log.Printf("[OK] %s\n", unblock)
		w.WriteHeader(204)
	}
}

func test(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(204)
}

func main() {
	http.HandleFunc("/block", blockTraffic)
	http.HandleFunc("/unblock", unblockTraffic)
	http.HandleFunc("/test", test)

	http.ListenAndServe(port, nil)
}

